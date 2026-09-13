# aaPanel (BaoTa) Software Installer — Implementation Research for EpicPanel

Agent 2 research deliverable. Sources:

- aaPanel clone: `/tmp/opencode/aapanel-research/baota` (github.com/aaPanel/BaoTa, depth-1, ~134 MB, clone OK)
- CDN install scripts fetched live: `/tmp/opencode/aapanel-research/scripts/t{0,1,3,4}/*.sh` (from `https://download.bt.cn/install/<type>/<name>.sh`)
- EpicPanel stable views: `git show HEAD:backend/internal/agent/{runtime_ops,executor,fpm,pkgr,extension_ops,worker,client}.go`, `git show HEAD:backend/internal/runtimes/{handler,store}.go`, `git show HEAD:backend/migrations/0003_websites_jobs.sql`, `0004_runtimes.sql`

---

## 1. How aaPanel actually works

### 1.0 Architecture in one paragraph

aaPanel is a Flask panel (`/www/server/panel`) + a detached task daemon (`BT-Task` → `task.py`) + a *remote script CDN* (`download.bt.cn` et al.). The panel never contains the install logic itself: it queues a shell one-liner into an SQLite task table, the daemon picks it up serially, and the one-liner (`install_soft.sh`) downloads the *real* per-software bash script from the CDN and executes it as root. All state (catalog, queue, logs) is local files/SQLite; the catalog *content* comes from `api.bt.cn` behind a compiled (`PluginLoader.*.so`, closed-source) loader, with a static seed shipped in git.

### 1.1 Software catalog

Three layers:

1. **Static seed in git** — `config/default_soft_list.conf`, mirrored at runtime to `data/softList.conf` (a cached JSON array, refreshed from the cloud and rewritten by `script/flush_soft.py` → compiled `PluginLoader.get_soft_list(cid)`; panel caches the cloud result 1 h: `class/panelPlugin.py:1298-1301`). Entry shape (`data/softList.conf`):

```json
{"name":"PHP",
 "versions":[{"status":false,"version":"8.2"},{"status":false,"version":"7.4"}, ...],
 "type":"语言解释器",                     // category ("Web服务器","数据库","FTP服务器",...)
 "msg":"若非必要，请安装更新的版本!",       // blurb
 "shell":"php.sh",                        // CDN script: /install/<type>/php.sh
 "check":"server/php/VERSION/bin/php"}    // install-detection path; VERSION substituted per version
```

`install_async` / `get_soft_find` substitute `{VERSION}` into `install_checks` (`class/panelPlugin.py:2130`) and mark `setup = os.path.exists(install_checks)` (`:2175`, `:2209`) — **installed-state detection is pure filesystem probing**, no package DB.

2. **Cloud catalog** — `POST https://api.bt.cn/panel/get_soft_list` (plus `get_soft_list_status` change-check). Categories in `data/type.json` (全部 / 运行环境 / 系统工具 / 宝塔插件 / 付费插件).

3. **PHP extension library** — `data/phplib.json` / `phplib.conf`:

```json
{"name":"ionCube","versions":["52",...,"70"],       // PHP-version applicability
 "type":"脚本解密", "shell":"ioncube.sh",            // per-ext build script on CDN
 "check":"ioncube_loader_lin"}                       // .so probe under php/<ver>/lib/...
```

Version list is pinned per software (`config/php_versions.json` = `["52"..."85"]`; `t0/php.sh` pins full versions as bash vars, e.g. `php_81="8.1.32"` — **discovery is "hardcode latest patch in the CDN script; bump server-side"**).

### 1.2 Task queue (install/remove/upgrade tasks)

Two coexisting tables, both SQLite:

* **Legacy `tasks`** (`data/db/system.db`, cols `id,name,type,status,addtime,execstr` + later `msg`, `install_status` via `ALTER TABLE` in `_SoftService.task_table_rep()`, `task.py:1993-2007`) — used by software installs (`type='execshell'`).
* **New `task_list`** (`data/db/task.db`, `class/panelTask.py:33-44`):

```sql
CREATE TABLE IF NOT EXISTS `task_list` (
  `id` INTEGER PRIMARY KEY AUTOINCREMENT, `name` TEXT, `type` TEXT,
  `status` INTEGER, `shell` TEXT, `other` TEXT,
  `exectime` INTEGER, `endtime` INTEGER, `addtime` INTEGER );
```

`type` is an integer kind: `0 exec shell, 1 download, 2 unzip, 3 zip, 4 db backup, 5 db import, 6 site backup, 8 copy, 9/10 recycle-bin` (`class/panelTask.py:170-218`). **Status codes: `0` queued, `-1` running, `1` done.**

**Enqueue** (panel side, `class/panelPlugin.py:install_async:1061-1137`):

```python
execstr = "cd /www/server/panel/install && /bin/bash install_soft.sh {} {} {} {} {}".format(
    get.type, mtype, get.sName, get.version, ols_execstr)     # mtype: install|update
task_id = public.M('tasks').add('id,name,type,status,addtime,execstr',
    (None, mmsg+'['+sName+'-'+version+']', 'execshell', '0', now, execstr))
public.writeFile('/tmp/panelTask.pl', 'True')                  # "queue busy" flag
if no task with status == -1:                                  # daemon seems idle
    if /dev/shm/.panelTask.pl older than 600 s:  ExecShell("/www/server/panel/BT-Task")
```

**Worker** (daemon side, `task.py` `_BTTaskService`, lines 1710-1780): infinite loop, 1 s tick; wakes every ≤60 s or immediately when `/dev/shm/bt_task_now.pl` appears (set by `panelTask.create_task`); `do_once()` does `UPDATE task_list SET status='0' WHERE status='-1'` — **crash recovery = blanket reset of running rows → at-least-once retry** (hence scripts must be re-runnable, and they mostly are because they check file existence first). Then selects `status=0 ORDER BY id ASC` and executes tasks **strictly serially**, one `multiprocessing.Process` per task, `p.join()`:

```python
public.ExecShell(task_shell + ' &> ' + log_file)   # class/panelTask.py:182
```

No step model, no per-step status — a task is one bash invocation whose combined stdout/stderr is the log. Log files: `/www/server/panel/tmp/<task_id>.log` (new queue) or the global `/tmp/panelExec.log` (legacy installs — all software installs share one log file; panel wipes it only when the queue is idle, `panelPlugin.py:1113-1114`). Cancellation = `kill -9` on `ps aux|grep <execstr>` (`panelTask.remove_task:116-161`) — crude and racy.

There is **no retry with backoff**: a failed install stays failed (`install_status` recorded after post-check, see §1.8) and the user re-clicks.

### 1.3 Where downloads come from

`install/install_soft.sh` (25 lines, in git) is the whole "package manager":

```bash
serverUrl=$NODE_URL/install
wget --no-check-certificate -O lib.sh $serverUrl/$mtype/lib.sh   # once, on install
wget --no-check-certificate -O $name.sh $serverUrl/$mtype/$name.sh
[ "$actionType" == 'install' ] && bash lib.sh
bash $name.sh $actionType $version
```

* `$NODE_URL` — chosen at runtime by `install/public.sh:get_node_url()`: it curls `/net_test` against 9 mirrors (`dg2.bt.cn`, `download.bt.cn`, `ctcc1/2-node.bt.cn`, `cmcc1-node.bt.cn`, `hk1-node.bt.cn`, `na1-node.bt.cn`, `jp1-node.bt.cn`, `cf1-node.aapanel.com`), keeps candidates with <300 ms, sorts by throughput and picks the fastest; fallback `https://download.bt.cn`. Geo-ordered lists via `data/domestic_ip.pl` / `foreign_ip.pl`.
* `$mtype` (arg 1) = **build method**: `0` = from source (yum world), `1` = binary/rpm package, `3` = source-style script for apt systems, `4` = dpkg/binary path for apt. The panel remaps on Debian/Ubuntu (`panelPlugin.py:1093-1097`): `0→3`, `1→4`. Verified: `t0/*` and `t3/*` fetched from CDN are byte-identical for php/mysql/nginx (the scripts branch on `PM=apt-get|yum` internally); `t1/mysql.sh` is a genuine *rpm* path (`rpm -ivh ${download_Url}/rpm/centos${OS_V}/${Is_64bit}/bt-${sqlVersion}.rpm --force --nodeps`) leaving `rpm.pl`/`deb.pl` marker files in the install dir so uninstall re-delegates to the package manager.
* Dependency tarballs: `${download_Url}/src/…` (php-x.y.z.tar.gz, redis-7.x.tar.gz, mysql-boost, curl-7.70, openssl-1.0.2u, icu, oniguruma, libsodium, cmake-3.31 …) — aaPanel re-hosts **all** upstream tarballs on its own CDN.
* Prebuilt distro-specific binaries for fast path: `${download_Url}/soft/redis/${OS_NAME}-${OS_V}-redis-${version}.tar.gz` (`t0/redis.sh:208`), falling back to source compile if the binary won't run (`t0/redis.sh:211-218` — exec test, then rebuild).
* Service units: `${download_Url}/init/systemd/{mysqld,redis}.service`, `${download_Url}/init/init7.redis` (SysV).
* Plugins (paid/free add-ons): `POST https://api.bt.cn/down/download_plugin` with user credentials → authenticated zip stream, saved `/www/server/panel/tmp/<name>.zip` (`panelPlugin.py __download_plugin:300-400`). Per-plugin `install.sh` under `${NODE_URL}/install/plugin/<name>/install.sh`.

### 1.4 PHP + extension compilation (the crown jewel)

`t0/php.sh` (1222 lines) runs `check_limit; Env_check; System_Lib; Install_Openssl_1_0_2; Install_Curl; Install_Icu4c; Configure_Get; Download_Src; Install_Configure; Install_PHP; Install_Zip_ext; Ln_PHP_Bin; Create_Fpm; Set_PHP_FPM_Opt; Set_Phpini; Download_Conf; Install_Zend; Pear_Pecl_Set; Install_Composer; Service_Add; Remove_Src`.

Configure line (PHP ≥ 7.2, `t0/php.sh:945`, condensed):

```bash
./configure --prefix=${php_setup_path} --with-config-file-path=${php_setup_path}/etc \
  --enable-fpm --with-fpm-user=www --with-fpm-group=www \
  --enable-mysqlnd --with-mysqli=mysqlnd --with-pdo-mysql=mysqlnd \
  --with-curl=${withCurl} --with-openssl=${withOpenssl} --with-sodium=/usr/local/libsodium \
  --enable-mbregex --enable-mbstring --enable-intl --enable-pcntl --enable-ftp \
  --enable-gd --with-mhash --enable-sockets --with-xmlrpc --enable-soap \
  --with-gettext --disable-fileinfo --enable-opcache --with-webp-dir=/usr
make ZEND_EXTRA_LIBS='-liconv' -j${cpuCore} 2>&1 | tee /tmp/php_make.pl
```

Notable: it compiles private openssl-1.0.2u/curl to satisfy ancient PHP; on Ubuntu 26/Debian 13 it forces `CC=gcc-13` (`Env_check`); `make -j` parallelism comes from a CPU-busy probe in `public.sh` (uses n-1 cores when idle). Zip ext is built from the PHP source tree via `phpize; ./configure --with-php-config=…; make && make install`, appended as `extension = zip.so` (`t0/php.sh:1033-1067`). FPM: `cp sapi/fpm/init.d.php-fpm /etc/init.d/php-fpm-${php_version}`; `listen = /tmp/php-cgi-${php_version}.sock`; pool `pm.max_children` tiered by RAM buckets (30/50/100/150/300, `:675-702`); nginx snippet `enable-php-<ver>.conf` auto-generated for versions 52–85 (`Download_Conf`).

Extensions from the UI: `class/config.py:1124`:

```python
pecl_result = public.ExecShell("yes ''|/www/server/php/{}/bin/pecl install {} | tee /tmp/pecl-{}.log".format(phpv, name, phpv))[0]
# "install ok" → success ; "already installed" → ok ; "No releases available for package" → friendly error
```

Enable/disable is php.ini surgery (`config.py:1817`): check `redis.so` present under `lib/php/extensions/no-debug-non-zts-<API>/`, toggle `extension=` line. ionCube/Zend: download vendor prebuilt loader `.so` via per-ext CDN shell (`phplib.json` `shell` field). Multi-version CLI plumbing in `class/jobs.py:set_php_cli_env()`: symlinks `/usr/bin/php{ver}-{phpize,pecl,pear,php-fpm,composer}` + `alias php{ver}=…` in `/root/.bashrc`, cleaned on uninstall.

### 1.5 Version coexistence

`/www/server/php/{52..85}/` fully independent prefixes (bin/etc/lib/src/var). One php-fpm master per version (`/etc/init.d/php-fpm-74`), one unix socket per version (`/tmp/php-cgi-74.sock`), per-version `php.ini`/`php-cli.ini`. "Default" PHP = symlink `/usr/bin/php → highest installed version dir` (re-pointed on uninstall, `t0/php.sh:Uninstall_PHP`). MySQL/Redis/Nginx/Apache are **singleton** directories (`/www/server/mysql`, `/www/server/redis` …) — no parallel DB versions (upgrade in place or uninstall-first; `install_async:1068-1070` refuses a second MySQL: "已安装MySQL数据库,请先卸载!").

### 1.6 Service naming & init

Legacy SysV-first, systemd grafted on: `php-fpm-74`, `mysqld`, `nginx`, `httpd`, `redis`, `pure-ftpd` under `/etc/init.d/` (plus fetched `*.service` units in `/usr/lib/systemd/system/` when `/run/systemd/system` exists). Web reload abstraction: `public.ServiceReload()` probes installed dirs to decide `/etc/init.d/nginx reload` vs `apachectl` vs `lswsctrl restart` (`class/public.py:597-610`).

### 1.7 Conflict & port management

* Web server choice is *inferred from the filesystem* (`GetWebServer`: openlitespeed binary → apache dir → nginx dir → default nginx, `public.py:577-594`); there is no config key. Apache and nginx can coexist on disk but **fight for 80/443 at runtime**; aaPanel's LAMP/LNMP one-click only installs one, and `installApache`-equivalent scripts stop the other. EpicPanel already handles this explicitly (see §2 mapping — apache install ends `systemctl stop && disable apache2`).
* Port-in-use is diagnosed **post-failure** by regex-matching the install log (`config/install_check.json` → nginx entry: `{"regexp":"bind\\(\\)\\s+to\\s+\\S+:(80|888|443)\\s+failed","msg":"nginx默认端口80已被占用…","status":0}`).

### 1.8 Preflight & post-install verification

Preflight is scattered and thin: `check_sys_write()` (detects other hardening agents before uninstall, `panelPlugin.py:1144`), OS-support matrix inside each script (e.g. `t0/mysql.sh:167-185` rejects MySQL 5.5/5.6 on Debian 12/Ubuntu 22/EL9 with an echo+exit), `install/<soft>_mem_kill.pl` / `<soft>_not_support.pl` marker files consulted by `check_install_status`, gcc/cmake/mem info printed into logs (`GetSysInfo` in `public.sh`).

Post-install verification is the strong part — `config/install_check.json` (14 keys) defines per-software probes with `{SetupPath}`/`{Version}` templating:

```json
"php": {"files_exists": ["{SetupPath}/php/{Version}/bin/php",
                         "{SetupPath}/php/{Version}/etc/php.ini",
                         "/etc/init.d/php-fpm-{Version}"],
        "pid": "{SetupPath}/php/{Version}/var/run/php-fpm.pid",
        "cmd": [{"exec": "/etc/init.d/php-fpm-{Version} status", "success": "is running"}]}
```

Evaluated in `_SoftService.check_install_status` (`task.py:2100+`): files → pid liveness via `/proc/<pid>/cmdline` → command output regex → log-regex rules. Outcome written to `tasks.msg` + `tasks.install_status`; `/tmp/panelExec.log` archived to `logs/installed/<soft>_<ts>.log` and linked into the UI message box (`save_installed_msg`/`update_installed_msg`).

### 1.9 Removal / upgrade

Uninstall = `install_soft.sh <type> uninstall <name> <version>` → script's `Uninstall_*`: stop+delete init.d, `rm -rf /www/server/php/$ver` (per version!) or the whole `/www/server/mysql`, `apt-get remove bt-php74` when the `deb.pl` marker exists, re-point `/usr/bin/php` symlink, refresh phpMyAdmin vhost. MySQL uninstall is **guarded**: refuses while `SELECT count(databases)>0` and tells the user to back up first (`panelPlugin.py:1205-1211`). Plugin add-ons: run plugin's `uninstall.sh` then `rm -rf /www/server/panel/plugin/<name>`. Upgrade = same scripts, `actionType=update` (dir `update/` on CDN) plus in-place `make install` semantics; PHP upgrades reinstall the whole version; a global restart hint via `data/restart.pl`.

### 1.10 Frontend polling

Flask routes `/soft`, `/task` dispatch `action=` to methods (`BTPanel/__init__.py:1077,1377`; allowlist at `panelPlugin.py:1384-1399`). The UI's task dock polls `GET /task?action=get_task_lists&status=-3&num=15` (≤10 pending+running rows, `id ASC`), and for each running row embeds `tail -n N /www/server/panel/tmp/<id>.log` (`get_task_log`, `panelTask.py:327-405`); downloads get a parsed wget progress object `{total,used,pre,speed,time}` (median of last speeds). Finished install logs move into the msg-box. Poll interval is client-side JS (~2 s). No websockets for tasks (only for terminal/monitor features).

---

## 2. Strengths / weaknesses → EpicPanel fit

**Strengths worth copying**

1. Decoupled enqueue → serialized daemon → generic `execshell` task model: trivially resumable, survives panel restarts (status reset on boot).
2. *Filesystem-as-truth* installed-state probing (`install_checks` templates) — cheap, robust, no metadata drift.
3. Declarative post-install verification (`install_check.json`: files + pid + cmd regex + log regex) turned into user-visible pass/fail messages.
4. Per-version prefixes + sockets + init services; symlink re-pointing on uninstall; `php{ver}-{phpize,pecl}` symlink farm.
5. Build-method ladder (source / distro package / prebuilt-tarball) with automatic binary→source fallback (redis).
6. Mirror speed-testing for CDN selection (matters for global fleets).
7. RAM-tiered FPM pool defaults; OS-support matrix inside scripts; `disable_functions` baseline.
8. Data-safety guard before uninstalling the DB engine.

**Weaknesses EpicPanel must NOT copy**

1. **Security**: `wget --no-check-certificate`, no checksum/signature on executable scripts, `curl|bash` panel self-update, root-run arbitrary remote bash, telemetry/anti-piracy pings (`bt_check`/`notpro`). EpicPanel has a registered agent + control plane; commands must be *typed, allow-listed jobs* — which `executor.go` already guarantees ("It never runs a shell": `exec.Command` with argv).
2. **Global `/tmp/panelExec.log`** shared by all installs → interleaved logs, races; task cancel = `kill -9` greps; retry = whole-script rerun.
3. Catalog logic (`PluginLoader`) is closed-source `.so` — the "open source" repo can't fully install anything without bt.cn's CDN. EpicPanel's catalog must be fully open & self-hosted.
4. No step model / no progress percent (the UI fakes it); no idempotency contract, scripts `grep` their own success patterns.
5. SQLite + HTTP polling doesn't survive multi-server fleets; EpicPanel already has PG `jobs` + agent claim/report channels (correct choice).
6. Hardcoded latest patch versions in scripts = silent staleness; mirrors are a single org's CDN.
7. Web-server coexistence handled by convention/marker files, not state; state is inferred from paths (`GetWebServer`) → brittle.

---

## 3. Mapping onto EpicPanel today (HEAD)

| aaPanel concept | EpicPanel equivalent (existing) | Gap |
|---|---|---|
| `tasks`/`task_list` sqlite + BT-Task daemon | PG `jobs` table (0003) + agent `worker.go` claim/dispatch (`case "install_runtime"` etc.) + `client.ReportProgress(jobID, percent, step)` | No log-stream capture into `jobs`; only percent+step |
| `install_soft.sh <type> …` | `Executor.InstallRuntime` switch (`runtime_ops.go:30`) | Missing mysql/mariadb/redis/nginx/pma/docker/wp cases (some exist: `webserver_ops.go`, `wp_ops.go`, `database_ops.go`) |
| Build-method ladder | already: apt → ondrej PPA (`addOndrejPPA`) → static-php-cli (`installStaticPHP`, dl.static-php.dev); node NodeSource → nodejs.org tarball; python distro → deadsnakes → astral-sh standalone; go via go.dev JSON API (`latestGoVersion`) | PPA chain lives inline in `runtime_ops.go` (no `ppa.go` in HEAD); ladder is per-runtime copy-paste, not data-driven |
| Catalog (softList.conf) | hardcoded `runtimes.Type` enum + `versionRe` validation in `runtimes/store.go`; CHECK constraint limits `type IN ('php','node','python','go')` (+ apache/OLS/java in code, 0033) | No DB catalog table, no seed data, no install-method chain description, no per-server capability |
| `install_checks` probing | `extension_ops.go DetectSoftware` (`DetectedSoftware` struct, binary path+`singleLineVersion`) | Same idea; needs generalizing to catalog rows |
| `/www/server/php/74` coexistence | `/opt/epicpanel/php-static/<major>`, `/opt/epicpanel/python-standalone/<major>`, `/usr/local/lib/nodejs/<stem>`, `/usr/local/go-<full>` + unit `php<major>-fpm` (`phpFpmService`) + sockets under `/run/epicpanel/php-fpm` | Two conventions (apt-installs use distro paths; managed use `/opt/epicpanel`) — unify below |
| Panel polling `/task?action=get_task_lists` | runtimes REST (`internal/runtimes/handler.go`): `GET/POST/DELETE /v1/organizations/{org}/servers/{server}/runtimes[/{id}][/extensions]`, `POST …/detect-software` | No generic `…/software` or `…/tasks/:id` surface |
| FPM pool per version | `fpm.go` EnsurePool/RemovePool + `CleanupOtherVersionPools` | already better (per-site pools, validated config, budgeted restarts) |
| MySQL/Redis via source build | `database_ops.go` (DBs on top of an existing server) | **No DB-server installer at all** — biggest new surface |

Existing seeds for extension mapping: `extension_ops.extensionPackage(rtType, major, name)` already prefers distro packages (`php8.3-redis` etc.) and falls back to `pecl`/source (`InstallPHPExtension`, `:57`) — same ladder as aaPanel but package-first.

---

## 4. Recommended EpicPanel design

### 4.1 Software catalog (PG tables + seed)

New migration `backend/migrations/00xx_software_catalog.sql`:

```sql
CREATE TABLE software_catalog (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name          TEXT NOT NULL,                 -- 'php','mysql','mariadb','redis','nginx','apache',
                                               -- 'nodejs','python','go','java','docker','pure-ftpd',
                                               -- 'phpmyadmin','adminer','wordpress','openlitespeed','memcached','mongodb','postgresql'
  category      TEXT NOT NULL,                 -- 'webserver','database','language','cms','tooling','cache'
  display_name  TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  singleton     BOOLEAN NOT NULL DEFAULT false,-- false for php/nodejs/python/go/java; true for mysql,redis,nginx,...
  probe_paths   JSONB NOT NULL DEFAULT '[]',   -- ["/usr/sbin/nginx","/opt/epicpanel/runtimes/{name}/{version}/bin/…"] ({version} templated, aaPanel-style)
  probe_cmd     TEXT NOT NULL DEFAULT '',      -- optional "--version" probe, like extension_ops.singleLineVersion
  verify        JSONB NOT NULL DEFAULT '{}',   -- aaPanel install_check.json clone:
                                               -- {"files_exists":[…],"systemd_unit":"redis","port":6379,"log_regex":[…]}
  requires      JSONB NOT NULL DEFAULT '[]',   -- e.g. wordpress → [{"name":"php"},{"name":"mysql"}]
  conflicts     JSONB NOT NULL DEFAULT '[]',   -- nginx ↔ apache for port 80 (advisory, not blocking)
  default_version TEXT NOT NULL,
  UNIQUE (name)
);

CREATE TABLE software_catalog_versions (
  catalog_id  UUID NOT NULL REFERENCES software_catalog(id) ON DELETE CASCADE,
  version     TEXT NOT NULL,                   -- "8.3", "22", "8.0" (major or major.minor per name)
  methods     JSONB NOT NULL,                  -- ORDERED install chain, first-success wins:
    -- php 8.3:  [{"kind":"apt","packages":["php8.3-fpm","php8.3-cli",…]},
    --            {"kind":"ppa","ppa":"ondrej/php","pin_suite":true},
    --            {"kind":"tarball","discover":"static_php","match":"php-8.3.*-fpm-linux-{arch}.tar.gz","sha256":null},
    --            {"kind":"source","tarball":"https://www.php.net/distributions/php-{ver}.tar.gz","configure":[…flags…] }]
    -- mysql 8.0: [{"kind":"apt_repo","key_pin":"…oracle…","repo":"mysql-8.0"},
    --            {"kind":"tarball","discover":"mysql_cdn","…"},{"kind":"source"}]
  eol         BOOLEAN NOT NULL DEFAULT false,
  PRIMARY KEY (catalog_id, version)
);
```

Seed data goes in the same migration (`INSERT … ON CONFLICT DO NOTHING`) — versions + method chains from §6's table. Control plane re-resolves "latest patch" at enqueue time (discovery strategies §6), so seeds age gracefully; a `software_catalog_refresh` scheduler tick (like `scheduler.go`) can auto-EOL-bump rows. Do NOT replicate aaPanel's remote-catalog dependency: catalog is local truth, self-updatable by releases.

Extend `jobs` type enum: `ALTER TYPE job_type ADD VALUE 'install_software'|'remove_software'|'upgrade_software'|'install_stack';` (keep existing `install_runtime`/`install_extension` for backward compat; `install_software` supersedes and internally routes runtime names to the proven `InstallRuntime` code path — zero regression for website provisioning).

### 4.2 Idempotent task engine (agent)

File placement:

```
backend/internal/agent/
  software.go            -- ExecuteSoftware(ctx, job) dispatcher; method-chain runner
  software_catalog.go    -- compiled-in fallback catalog (embed) used when agent
                           -- payload omits resolved methods (offline agents)
  software_preflight.go  -- disk/RAM/port/apt-dpkg-lock checks (§4.5)
  software_php.go        -- php + extensions (wraps installPHP/installStaticPHP/extension_ops)
  software_db.go         -- mysql/mariadb/redis (new; per-server singleton, data-safety guard)
  software_web.go        -- nginx/apache/openlitespeed (wraps webserver_ops.go + mutual-stop)
  software_stack.go      -- one-click LNMP/LAMP: ordered subtasks, per-step logs
  software_cleanup.go    -- remove: stop+disable unit, purge/rm tree, re-point symlinks
backend/internal/software/
  catalog.go  handler.go store.go        -- control-plane package (mirror of runtimes/)
backend/migrations/00xx_software_catalog.sql
backend/migrations/00yy_software_logs.sql   -- jobs.log_lines JSONB + log_seq BIGINT (append delta)
```

Engine rules (aaPanel lessons applied):

1. **Steps, not one-blob**: a job = ordered `[]Step{Name, Args []string, Check func, SkipIfInstalled}` persisted progress via `ReportProgress(jobID, percent, "step: …")`; each step is idempotent (existence checks first, like `installPHP` short-circuit) so a retry converges — same contract `executor.go` already uses for `ProvisionWebsite`.
2. **Log capture**: run commands with `cmd.Std*Pipe`, scan lines, redact `password|secret|token=`, ship bounded tail (last ~200 lines, 8 KB chunks) as `ResultRequest` log deltas; control plane appends to `jobs` (`result->'logs'`) + fan-out to the UI. This beats aaPanel's shared `/tmp/panelExec.log`.
3. **Serialization per server, concurrency across servers**: already guaranteed by the one-job-per-agent claim loop; add a *software mutex* so two installs on the same box never overlap (apt/dpkg lock + port races) — reject a second `install_software` while one is `running` (409). aaPanel's crash-reset ≈ our existing `claimed_at` timeout requeue in `jobs/store.go`; keep at-least-once (steps are idempotent).
4. **Verification step (mandatory last step)**: run the catalog `verify` block (unit `is-active`, `files_exists` templated by version, optional port listen, log_regex fallback) and surface aaPanel-style human messages ("port 80 already in use") in `error_message`.
5. **Uninstall guard**: mirror aaPanel's MySQL rule — `remove_software mysql` 409s while `server_databases` rows reference the server or `mysql` user DBs exist unless `force=true`; cleanup = stop+disable unit, purge packages (apt-managed) **or** `rm -rf` the managed prefix + unit file + symlinks (tarball-managed), re-point `/usr/local/bin/<name>` to highest remaining version (php.go `removeStaticPHP` already does this); never touch `/var/lib/mysql` data dir without explicit `destroy_data=true`.
6. **Upgrade path**: `upgrade_software` = snapshot step (config backup under `/etc/epicpanel/backup/`), install-new (coexisting for versioned runtimes), re-point symlink/`CleanupOtherVersionPools` for php-fpm, validate config (`php-fpm -t`, `nginx -t`, `mysqld --check`), keep old for rollback one minor. For singletons (mysql/mariadb) ship dump→stop→swap-bins→`mysql_upgrade`→start, refusing cross-major by default.

### 4.3 Version coexistence scheme

* **Distro-package-first**: when apt/dnf provides the exact version (incl. PPAs), install it there (path of least maintenance, security updates for free) — EpicPanel's established preference, unlike aaPanel's source-everything default.
* **Managed fallback** under a single root: `/opt/epicpanel/runtimes/<name>/<version>/` (bin/, etc/, lib/, logs/) — generalize the existing `/opt/epicpanel/php-static/8.3` and `python-standalone/3.12` layouts to this one scheme (`php/8.3`, `node/22.14.0`, `python/3.12`, `go/1.23.6`, `jdk/21.0.6`, `redis/7.2.7`, `mariadb/11.4`); ship a one-shot `software.go` migration step that relocates/symlinks the two legacy prefixes. (Task brief suggested `/usr/local/epicpanel/runtimes`; EpicPanel code today writes `/opt/epicpanel` — recommend keeping `/opt` to avoid churn, expose the root as a constant `managedRoot`.)
* **Exposure**: per-version symlinks `/usr/local/bin/<bin><version-suffix>` (e.g. `php8.3`, `node22`) + a `current` link per name; **PATH default only set when nothing provides it** (distro wins), replicating aaPanel's `/usr/bin/php` re-point logic but atomically (`os.Rename` of tmp symlink).
* **Per-version systemd units**: template units (`epic-php-8.3-fpm.service`, `epic-redis.service` for managed builds) installed from agent-embedded templates (`//go:embed templates/*.service`) — never fetched from a URL (aaPanel fetches `init/systemd/*.service` remotely: reject that pattern). Distro installs reuse distro units (`php8.3-fpm`, `mysql`, `redis-server`).
* **FPM/nginx wiring**: keep `fpm.go` pool machinery and `/run/epicpanel/php-fpm/<ver>…sock` (already superior to aaPanel's `/tmp/php-cgi-*.sock`); managed php-fpm units get `RuntimeDirectory=epicpanel/php-fpm`.

### 4.4 API contract (control plane)

Registered beside the existing runtimes routes (`handler.go Register`), org-role gated as there:

```
GET  /api/v1/organizations/{org_id}/servers/{server_id}/software[?category=&q=]
     → 200 {"data":[{"name":"php","display_name":"PHP","category":"language",
          "singleton":false,"installed":[{"version":"8.3","method":"apt","status":"available","healthy":true}],
          "available_versions":[{"version":"8.4","methods":["apt","ppa","tarball"],"eol":false}],
          "requires":[],"conflicts":["apache"]}, …]}

GET  /api/v1/…/software/{name}
     → 200 {"name":"mysql","installed_version":null,
            "versions":[{"version":"8.0","methods":[{"kind":"apt_repo","package":"mysql-server"},…]}]}

POST /api/v1/…/software/install
     {"name":"php","version":"8.3","method":"auto"}          # auto = catalog chain order
     → 202 {"task_id":"<uuid>","status":"queued"}            # rows in jobs; 409 if busy/unmet deps

POST /api/v1/…/software/remove   {"name":"redis","version":"","force":false}   → 202/409
POST /api/v1/…/software/upgrade  {"name":"php","from":"8.2","to":"8.3"}        → 202
POST /api/v1/…/software/stack    {"stack":"lnmp","components":{"web":"nginx","php":"8.3","db":"mysql","cache":"redis"}}
     → 202 {"task_id":"…","steps_expected":4}                 # ordered plan, abort-on-fail with completed-steps report

POST /api/v1/…/software/{name}/extensions   {"version":"8.3","extension":"redis","action":"install"}
     → 202   # reuses existing install_extension job type

GET  /api/v1/…/software/tasks/{task_id}
     → 200 {"id":"…","type":"install_software","status":"running|success|failed|queued",
            "name":"php","version":"8.3","percent":55,"step":"Compiling extensions…",
            "steps":[{"name":"preflight","status":"done"},{"name":"apt","status":"running","percent":40},…],
            "error":"","created_at":"…","started_at":"…","finished_at":null,
            "log_cursor":214}
GET  /api/v1/…/software/tasks/{task_id}/logs?cursor=214&max=400
     → 200 {"cursor":231,"done":false,"lines":["(23ms) apt-get install php8.3-fpm","…"]}
```

UI keeps polling `/software/tasks/:id` + `logs?cursor=` (aaPanel's proven UX) — or, later, piggybacks the existing agent `Frame` stream (`stream.go` already replays missed frames with sequence numbers; add a `job_log` frame kind so logs are push, poll as fallback).

### 4.5 Preflight (new `software_preflight.go`)

Gate every install/remove job *before* mutation, with explicit, localized errors (aaPanel mostly discovers failures post-hoc — improvement):
`apt-get`/`dpkg -l` lock free; disk: ≥1.5× estimated download+build size on `/` and `/opt` (per-method estimate in catalog); RAM: php source ≥1 GB, mysql ≥2 GB, ≥0.5 GB free else force package method; ports: 80/443 (nginx vs apache vs lsws), 3306, 6379, 21 — detect listener and name the owner process; SELinux/apparmor not enforcing on managed prefixes; systemd present (units depend on it); arch/os-release in the method's support matrix (else jump to the next method in the chain — this turns aaPanel's in-script OS matrix into data).

### 4.6 Security notes (hard requirements — where aaPanel is worst)

1. **No remote script execution ever.** Catalog `source` steps pin tarball URLs; agent downloads with `curl --proto '=https'` (no `-k`, no `--no-check-certificate`), verifies **SHA-256 from the catalog** (seed from official `…sha256sum.asc` / `.json` endpoints at seed time; refresh in CI). `sha256:null` ⇒ refuse unless `allow_unpinned=true` server flag.
2. **PPA/repos**: pin GPG fingerprints, not keyserver luck — ondrej `14AA40EC0831D867E199F231F24AEA9FB05498B7`, deadsnakes `F2EEC42A5C03BA92F8B40A6F196695C091BE1EA1`, NodeSource `BB57688F5B5A5C66E0DD5A3E5F43797F...` (resolve official fingerprint at seed), Redis `packages.redis.io/gpg`, MySQL Oracle release-key A8D5E858…, MariaDB `deb.mariadb.org` mirror-sign. Write `Signed-By: /etc/apt/keyrings/epicpanel-<x>.gpg` + `URIs:`-pin `Files:`-restricted `.sources` files (EpicPanel already writes ondrej sources in `addOndrejPPA` — add fingerprint check there).
3. **Checksum upstream discovery**: Adoptium publishes `.sha256` next to binaries; nodejs.org publishes `SHASUMS256.txt`; go.dev JSON carries `sha256`; python-build-standalone assets carry digests in release body → agent verifies before extracting. For static.app: pin per-release digest via the GitHub API asset + CI-refreshed catalog.
4. **Typed argv everywhere** (keep `executor.go` discipline): the catalog's `configure` flag list and `packages` arrays are data passed as separate argv entries — never interpolated into `bash -c`. (Current `installNode` pipes NodeSource's `setup_22.x` through `bash -c` — replace with a catalog `apt_repo` method writing the deb822 file directly, or vendor the repo key + list generation in Go.)
5. Jobs payload must not carry secrets to the agent except DB root creds for backup steps — keep the existing per-job token path; redact `PASS|SECRET|TOKEN=` from captured logs; 0600 on all generated confs (my.cnf, redis.conf include `requirepass`).
6. Least privilege: unit files get `ProtectSystem=strict` + `ReadWritePaths=` for managed prefixes where upstream supports it (redis, php-fpm already); `User=www-data` for php-fpm pools (existing).
7. Extensions: pecl only over HTTPS with `--set-number` answers fixed (`yes ''|` heredoc replacement = fixed `-D`/`-c` flags), `extension_dir` validated before appending `extension=` to php.ini (aaPanel appends blindly to php.ini — injection sink if ext name is user input; EpicPanel validates via `extensionPackage()` allow-list map: extend it).

### 4.7 Phased implementation plan

**P1 — catalog + surface (control plane only, no behavior change)**
`00xx_software_catalog.sql` (schema + seed for the 12 already-working runtimes/extensions chains), `backend/internal/software/{catalog,store,handler}.go`, routes registered in `api/server.go`, unit tests in `api/software_test.go`. Frontend consumes catalog later.

**P2 — unified task engine + logs**
`jobs` enum extension + `log_lines/log_seq` columns (`00yy_software_logs.sql`), `agent/worker.go` `case "install_software"/"remove_software"/"upgrade_software"`, `agent/software.go` runner (wraps existing `InstallRuntime`/`RemoveRuntime` for php/node/python/go/apache/litespeed — deprecating nothing), log pipe+redact helper in `agent/stream.go` or `client.go`, `GET …/software/tasks/:id[/logs]`. Tests: fake clock/apt table-driven like `ppa_e2e_test.go` (`software_e2e_test.go`).

**P3 — preflight + verify engine**
`software_preflight.go`, catalog `verify` evaluation (`software.go:verify()`), errors mapped to API messages; port registry helper (extend `limits.go`/`scheduler.go` for 80/443 conflict advisory between nginx/apache).

**P4 — database servers (biggest gap)**
`software_db.go`: mariadb via distro/apt-repo method (`mariadb-server` / `deb.mariadb.org` pinned key), mysql via Oracle apt repo (pinned) with tarball fallback, redis via distro → `packages.redis.io` repo → static tarball (`redis.io` builds). Units `mysql`/`mariadb`/`redis-server`; my.cnf/redis.conf rendered from Go templates (`//go:embed templates/`), bind 127.0.0.1 default, socket+`requirepass`, `verify` blocks per §4.1; uninstall guards wired to `server_databases` rows. Integrates with existing `database_ops.go` (it currently assumes an external server) and `pma_sso.go`.

**P5 — nginx + remaining one-click**
managed nginx (`epic-nginx.service`, `/opt/epicpanel/runtimes/nginx/<ver>`) optional for versions distro lacks; keep distro default; `software_web.go` mutual-exclusion state (`webserver_preference` server setting replaces path-sniffing); phpMyAdmin/Adminer/WordPress become catalog rows reusing `InstallDatabaseTools`/`wp_ops.go` as their `method.kind`; `software_stack.go` LNMP/LAMP orchestrator; mirror selection = catalog `mirrors[]` with latency probe (Go port of `get_node_url`, optional, only for self-hosted CDN deployments).

**P6 — polish**
`upgrade_software` real flows (P42-style snapshot), EOL automation in `scheduler.go`, OpenResty/Tengine variants, per-version FPM tuning from RAM (reuse existing pool sizing), read-only "installed" sync into catalog probe results on heartbeat (`collector.go`).

---

## 5. Source & version-discovery table (per software)

HEAD-verified 2026-09-13 from this workstation; ✅ 200 / redirect ok.

| Software | Method chain (recommended) | Download endpoints | Version discovery | Verified |
|---|---|---|---|---|
| PHP (apt) | distro `phpX.Y-fpm` | Ubuntu/Debian main | `apt-cache policy` | n/a |
| PHP (PPA) | ondrej/php | `https://ppa.launchpadcontent.net/ondrej/php/ubuntu/` | probe `dists/<suite>/Release` (already `ppaHasSuite`) | ✅ |
| PHP (static) | static-php-cli | `https://dl.static-php.dev/static-php-cli/common/` (flat `php-X.Y.Z-{cli,fpm}-linux-{arch}.tar.gz`) | directory listing / GitHub releases of `static-php/static-php-cli-hosted` (agent already resolves via `resolveStaticPHPAsset`) | ✅ |
| PHP (source) | official tarballs | `https://www.php.net/distributions/php-X.Y.Z.tar.gz` (+`.sha256.asc`) | release JSON API `https://www.php.net/api/1/versions/release/latest-patch.php` or `releases.php?type=json` | ✅ (8.3.20) |
| PHP ext | apt `phpX.Y-<ext>` → pecl | `https://pecl.php.net/get/<ext>-<ver>.tgz` | PECL REST `https://pecl.php.net/rest/r/<ext>/allreleases.xml` | ✅ (redis-6.0.2.tgz) |
| MySQL | Oracle apt/yum repo → tarball | `https://dev.mysql.com/get/Downloads/MySQL-X.Y/mysql-X.Y.Z.tar.gz` (→ cdn.mysql.com); repo `https://repo.mysql.com/apt/debian` | HTML scrape of per-major dir or repo `Packages.gz` (no stable "latest" alias → pin via catalog refresh) | ✅ (8.4.6) |
| MariaDB | distro → `deb.mariadb.org` | `https://deb.mariadb.org/<maj.min>/ubuntu …`, `https://archive.mariadb.org/` | repo `Release`/Packages.gz | ✅ (archive listing) |
| Redis | distro → `packages.redis.io` → tarball | `https://packages.redis.io/deb` (key `…/gpg` ✅; bare dir listing 403s — expected), `https://download.redis.io/releases/redis-X.Y.Z.tar.gz` | `https://download.redis.io/releases/` listing or GitHub `redis/redis/tags` | ✅ (7.2.7) |
| Nginx | distro → nginx.org repo → official tarball | `https://nginx.org/packages/mainline/ubuntu/`, `https://nginx.org/download/nginx-X.Y.Z.tar.gz` | nginx.org `?C=…` listing / `https://api.github.com/repos/nginx/nginx/tags` | not probed (stable, well-known) |
| Apache | distro `apache2` only (v2.4) | `https://dlcdn.apache.org/httpd/httpd-X.Y.Z.tar.gz` | `https://dlcdn.apache.org/httpd/` dir listing (versions rotate off CDN) | not probed |
| Node.js | NodeSource repo → official tarball | `https://deb.nodesource.com/setup_22.x` (script; superseded by Go-generated deb822), `https://nodejs.org/dist/latest-v22.x/` + `SHASUMS256.txt` | HTML listing grep (agent already does this) / `https://nodejs.org/dist/index.json` (preferred, JSON) | ✅ |
| Python | distro → deadsnakes → astral-sh standalone | `https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu/`, `https://github.com/astral-sh/python-build-standalone/releases/latest/download/cpython-<v>+<date>-<arch>-unknown-linux-gnu-install_only.tar.gz` | GitHub API `releases/latest` + asset glob (already implemented) | ✅ both |
| Go | official tarball only | `https://go.dev/dl/goX.Y.Z.linux-<arch>.tar.gz` | `https://go.dev/dl/?mode=json&include=all` (has `sha256`) — already implemented | ✅ |
| Java | distro `openjdk-*` → Adoptium tarball | `https://api.adoptium.net/v3/assets/latest/21/hotspot?architecture=x64&image_type=jdk&os=linux` (JSON → binary + checksum) | Adoptium V3 API (best-in-class discovery) | ✅ |
| Docker | official apt repo / static | `https://download.docker.com/linux/static/stable/x86_64/docker-XX.Y.Z.tgz`, `…/linux/debian|ubuntu` repo | repo Packages.gz; static listing | ✅ (listing) |
| phpMyAdmin | tarball (PHP app) | `https://files.phpmyadmin.net/phpMyAdmin/5.2.3/phpMyAdmin-5.2.3-all-languages.tar.gz` — **GitHub releases attach no tarballs** (`…/releases/latest/download/…` 404s; asset naming differs per major line `phpMyAdmin-6-0-x-…` also 404) | `https://www.phpmyadmin.net/files/` listing or `https://api.github.com/repos/phpmyadmin/phpmyadmin/releases/latest` tag → files.phpmyadmin.net URL template | ✅ files host / ❌ GitHub assets |
| Adminer | single PHP file | `https://www.adminer.org/latest.php` (302 → versioned) | follow redirect | ✅ (302) |
| Composer | installer phar | `https://getcomposer.org/download/latest-stable/composer.phar` (+`.sha256`) | `https://getcomposer.org/versions` JSON | ✅ |
| ionCube loaders | vendor tarball | `https://downloads.ioncube.com/loader_downloads/ioncube_loaders_lin_x86-64.tar.gz` | no API — HTML page scrape, refresh via CI | ✅ |
| OpenLiteSpeed | vendor apt repo (already) | `https://rpms.litespeedtech.com/debian/` | repo Release | ✅ |
| aaPanel reference | — | `https://download.bt.cn/install/0/php.sh` ✅ (what we are replacing, not using) | — | ✅ |

**Failed/needs-attention in verification:** `https://packagecloud.io/redis/redis/gpgkey` → 404 (packagecloud path moved; use `https://packages.redis.io/gpg` ✅ instead); phpMyAdmin GitHub release assets → 404 (use files.phpmyadmin.net ✅); `https://packages.redis.io/deb/` root → 403 (directory browsing disabled; repo itself works — `dists/stable/Release` ✅).

**Stability verdict:** nodejs.org, go.dev, adoptium API, php.net, pecl, dl.static-php.dev, LaunchPad PPAs, download.redis.io, getcomposer.org, files.phpmyadmin.net, dev.mysql.com/get — stable, no custom mirror needed. MySQL tarballs need *exact patch* pinned in catalog (refresh job, or prefer Oracle apt repo). Anything bt.cn-hosted must never be a runtime dependency; EpicPanel's own mirror (object store) is the place for re-hosted vendor tarballs if fleet egress to 15 domains is a concern.

---

## 6. Ten-line summary of the recommendation

Catalog in PG with ordered `apt→ppa/repo→tarball→source` method chains per version; agent runs typed, stepwise, idempotent jobs on the existing `jobs`/claim channel with per-line redacted log deltas; `verify` blocks (aaPanel's best idea) make pass/fail explicit; managed coexistence under `/opt/epicpanel/runtimes/<name>/<version>` with symlink exposure and per-version units; preflight ports/disk/RAM up front; checksum + pinned keys everywhere (aaPanel's biggest negative space); ship new servers (mysql/mariadb/redis/nginx) behind the same contract while runtime types route through the proven existing code.
