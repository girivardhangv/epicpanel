#!/usr/bin/env php
<?php
/**
 * EpicPanel licensing server — admin CLI.
 *
 * Commands:
 *   php cli.php generate --plan <plan> --seats <N> --days <N> --name "<name>" --features "feat1,feat2"
 *   php cli.php list
 *   php cli.php revoke <key-or-id>
 *   php cli.php suspend <key-or-id>
 *   php cli.php unsuspend <key-or-id>
 *   php cli.php status <fingerprint>
 */

declare(strict_types=1);

// Safety: refuse to run over HTTP. This file manages licenses; it must only
// ever execute from a shell on the licensing server.
if (php_sapi_name() !== 'cli') {
    http_response_code(403);
    header('Content-Type: text/plain');
    echo "Forbidden: the licensing admin CLI can only be run from the shell.\n";
    exit(1);
}

require __DIR__ . '/lib.php';

$args = array_slice($argv ?? [], 1);
if (empty($args)) {
    echo "Usage:\n";
    echo "  php cli.php generate --plan starter --seats 1 --days 365 --name \"Client\" --features \"nginx,php,db\"\n";
    echo "  php cli.php list\n";
    echo "  php cli.php revoke <key-or-id>\n";
    echo "  php cli.php suspend <key-or-id>\n";
    echo "  php cli.php unsuspend <key-or-id>\n";
    echo "  php cli.php status <fingerprint>\n";
    exit(2);
}

$db = pdo();
$cmd = $args[0];
$opts = parse_params(array_slice($args, 1));

switch ($cmd) {
    case 'generate':
        cmd_generate($db, $opts);
        break;
    case 'list':
        cmd_list($db);
        break;
    case 'revoke':
        cmd_update($db, $opts[0] ?? '', 'revoked');
        break;
    case 'suspend':
        cmd_update($db, $opts[0] ?? '', 'suspended');
        break;
    case 'unsuspend':
        cmd_update($db, $opts[0] ?? '', 'active');
        break;
    case 'status':
        cmd_status($db, $opts[0] ?? '');
        break;
    default:
        echo "Unknown command: $cmd\n";
        exit(2);
}

function cmd_generate(PDO $db, array $opts): void
{
    $plan = $opts['--plan'] ?? 'starter';
    $seats = max(1, (int) ($opts['--seats'] ?? 1));
    $days = $opts['--days'] ?? null;
    $name = $opts['--name'] ?? '';
    $features = $opts['--features'] ?? 'nginx,php,db';
    $featuresArr = array_map('trim', explode(',', $features));

    $key = generate_key();
    $id = uuid();
    $expires = $days !== null ? expiry_from_days((int) $days) : null;

    $stmt = $db->prepare(
        'INSERT INTO licenses (id, key_hash, plan, seats, features, issued_to_name, expires_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)'
    );
    $stmt->execute([$id, key_hash($key), $plan, $seats, json_encode($featuresArr), $name, $expires]);

    echo "License created:\n";
    echo "  Key:    $key\n";
    echo "  Plan:   $plan\n";
    echo "  Seats:  $seats\n";
    echo "  Name:   $name\n";
    echo "  Expires: " . ($expires ?? 'never') . "\n";
    echo "  Features: " . implode(', ', $featuresArr) . "\n";
    echo "\n  Store this key securely — it will not be shown again.\n";
}

function cmd_list(PDO $db): void
{
    $rows = $db->query('SELECT * FROM licenses ORDER BY created_at DESC')->fetchAll(PDO::FETCH_ASSOC);
    if (empty($rows)) {
        echo "No licenses.\n";
        return;
    }
    printf("%-36s %-8s %-30s %-6s %-12s %s\n", 'ID', 'STATUS', 'NAME', 'SEATS', 'PLAN', 'EXPIRES');
    foreach ($rows as $r) {
        printf("%-36s %-8s %-30s %-6d %-12s %s\n",
            $r['id'], $r['status'], $r['issued_to_name'], $r['seats'], $r['plan'],
            $r['expires_at'] ?? 'never'
        );
    }
}

function cmd_update(PDO $db, string $keyOrID, string $status): void
{
    if ($keyOrID === '') {
        echo "Provide a key or ID.\n";
        exit(1);
    }
    $hash = key_hash($keyOrID);
    $stmt = $db->prepare('UPDATE licenses SET status = ? WHERE key_hash = ? OR id = ?');
    $stmt->execute([$status, $hash, $keyOrID]);
    $affected = $stmt->rowCount();
    if ($affected === 0) {
        echo "No matching license found.\n";
        exit(1);
    }
    echo "License updated to $status.\n";
}

function cmd_status(PDO $db, string $fingerprint): void
{
    if ($fingerprint === '') {
        echo "Provide a fingerprint.\n";
        exit(1);
    }
    $resp = validate_fingerprint($db, $fingerprint);
    if ($resp === null) {
        echo "No active license for this fingerprint.\n";
        exit(1);
    }
    echo json_encode($resp, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES) . "\n";
}

function uuid(): string
{
    $data = random_bytes(16);
    $data[6] = chr((ord($data[6]) & 0x0f) | 0x40);
    $data[8] = chr((ord($data[8]) & 0x3f) | 0x80);
    return vsprintf('%s%s-%s-%s-%s-%s%s%s', str_split(bin2hex($data), 4));
}

function parse_params(array $args): array
{
    $opts = [];
    $i = 0;
    while ($i < count($args)) {
        $a = $args[$i];
        if (str_starts_with($a, '--')) {
            if (str_contains($a, '=')) {
                [$k, $v] = explode('=', $a, 2);
                $opts[$k] = $v;
            } elseif ($i + 1 < count($args) && !str_starts_with($args[$i + 1], '--')) {
                $opts[$a] = $args[$i + 1];
                $i++;
            } else {
                $opts[$a] = true;
            }
        } else {
            $opts[] = $a;
        }
        $i++;
    }
    return $opts;
}