<?php
/**
 * EpicPanel licensing server — shared helpers.
 *
 * A dependency-free PHP + SQLite licensing backend that implements the exact
 * contract the EpicPanel panel expects:
 *
 *   POST /v1/activate    {license_key, fingerprint} -> LicenseResponse
 *   POST /v1/validate    {fingerprint}              -> LicenseResponse
 *   POST /v1/deactivate  {license_id, fingerprint}  -> {"ok":true}
 *
 * No Composer, no DB server — just PHP with the pdo_sqlite extension (bundled
 * by default in every distro PHP).
 */

declare(strict_types=1);

function db_path(): string
{
    return __DIR__ . '/var/licensing.db';
}

function pdo(): PDO
{
    static $pdo = null;
    if ($pdo === null) {
        $dir = dirname(db_path());
        if (!is_dir($dir)) {
            mkdir($dir, 0755, true);
        }
        $pdo = new PDO('sqlite:' . db_path());
        $pdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
        $pdo->exec('PRAGMA journal_mode=WAL;');
        schema($pdo);
    }
    return $pdo;
}

function schema(PDO $db): void
{
    $db->exec("CREATE TABLE IF NOT EXISTS licenses (
        id             TEXT PRIMARY KEY,
        key_hash       TEXT NOT NULL UNIQUE,
        plan           TEXT NOT NULL DEFAULT 'starter',
        seats          INTEGER NOT NULL DEFAULT 1,
        features       TEXT NOT NULL DEFAULT '[]',
        issued_to_name TEXT NOT NULL DEFAULT '',
        status         TEXT NOT NULL DEFAULT 'active',
        expires_at     TEXT,
        created_at     TEXT NOT NULL DEFAULT (datetime('now'))
    );");

    $db->exec("CREATE TABLE IF NOT EXISTS activations (
        license_id   TEXT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
        fingerprint  TEXT NOT NULL,
        activated_at TEXT NOT NULL DEFAULT (datetime('now')),
        last_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
        PRIMARY KEY (license_id, fingerprint)
    );");
}

/** Hash a license key for storage (we never keep plaintext keys). */
function key_hash(string $key): string
{
    return hash('sha256', strtoupper(trim($key)));
}

/**
 * Generate a license key like EPIC-XXXXX-XXXXX-XXXXX-XXXXX.
 * Uses a crypto-random alphabet that avoids ambiguous characters.
 */
function generate_key(): string
{
    $alpha = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'; // no I, O, 0, 1
    $parts = [];
    for ($g = 0; $g < 4; $g++) {
        $part = '';
        for ($i = 0; $i < 5; $i++) {
            $part .= $alpha[random_int(0, strlen($alpha) - 1)];
        }
        $parts[] = $part;
    }
    return 'EPIC-' . implode('-', $parts);
}

/** ISO8601 timestamp for now+days (null = lifetime license). */
function expiry_from_days(?int $days): ?string
{
    if ($days === null || $days <= 0) {
        return null;
    }
    return gmdate('Y-m-d\TH:i:s\Z', time() + $days * 86400);
}

/** Map a license row to the canonical LicenseResponse the panel parses. */
function to_response(array $license, array $activation): array
{
    $features = json_decode($license['features'] ?? '[]', true);
    if (!is_array($features)) {
        $features = [];
    }
    $status = $license['status'] ?? 'active';
    $now = time();

    if ($status === 'revoked') {
        $out = 'invalid';
        $msg = 'License has been revoked';
    } elseif ($status === 'suspended') {
        $out = 'suspended';
        $msg = 'License is suspended';
    } elseif ($license['expires_at'] !== null && strtotime($license['expires_at']) < $now) {
        $out = 'expired';
        $msg = 'License has expired';
    } else {
        $out = 'valid';
        $msg = 'License is valid';
    }

    $response = [
        'status'          => $out,
        'message'         => $msg,
        'license_id'      => $license['id'],
        'plan'            => $license['plan'],
        'seats'           => (int) $license['seats'],
        'features'        => $features,
        'issued_to_name'  => $license['issued_to_name'],
        'expires_at'      => $license['expires_at'],
    ];
    if ($activation !== null) {
        $response['activated_at'] = $activation['activated_at'];
    }
    return $response;
}

/** Validate + touch an activation for a fingerprint. Returns response or null. */
function validate_fingerprint(PDO $db, string $fingerprint): ?array
{
    $stmt = $db->prepare(
        'SELECT a.*, l.* FROM activations a
         JOIN licenses l ON l.id = a.license_id
         WHERE a.fingerprint = ?'
    );
    $stmt->execute([$fingerprint]);
    $row = $stmt->fetch(PDO::FETCH_ASSOC);
    if ($row === false) {
        return null;
    }
    $db->prepare('UPDATE activations SET last_seen_at = datetime(\'now\') WHERE license_id = ? AND fingerprint = ?')
        ->execute([$row['license_id'], $fingerprint]);
    return to_response($row, $row);
}

function json_out(array $payload, int $status = 200): never
{
    http_response_code($status);
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode($payload, JSON_UNESCAPED_SLASHES);
    exit;
}

function json_error(string $code, string $message, int $status = 400): never
{
    json_out(['error' => ['code' => $code, 'message' => $message]], $status);
}

function read_json_body(): array
{
    $raw = file_get_contents('php://input');
    $data = json_decode($raw ?: '{}', true);
    return is_array($data) ? $data : [];
}
