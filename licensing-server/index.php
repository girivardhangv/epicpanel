<?php
/**
 * EpicPanel licensing server — public API.
 *
 * Endpoints (all POST, JSON body):
 *   /v1/activate   {license_key, fingerprint}
 *   /v1/validate   {fingerprint}
 *   /v1/deactivate {license_id, fingerprint}
 *
 * Returns the LicenseResponse shape the EpicPanel panel expects:
 *   {status, message, license_id, plan, seats, features, issued_to_name, expires_at}
 *
 * Run with the PHP built-in server for dev:
 *   php -S 0.0.0.0:9911 -t licensing-server licensing-server/index.php
 * Or point nginx/Apache at this file (see README).
 */

declare(strict_types=1);

require __DIR__ . '/lib.php';

// CORS for panel access if the panel is served from a different origin.
header('Access-Control-Allow-Origin: *');
header('Access-Control-Allow-Headers: Content-Type, Authorization, X-Panel-Product');
if (($_SERVER['REQUEST_METHOD'] ?? '') === 'OPTIONS') {
    http_response_code(204);
    exit;
}

$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH) ?? '/';
$path = rtrim($path, '/');

if ($path === '/v1/health') {
    json_out(['ok' => true, 'service' => 'epicpanel-licensing']);
}

if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST') {
    json_error('METHOD_NOT_ALLOWED', 'Use POST', 405);
}

$db = pdo();
$body = read_json_body();

switch ($path) {
    case '/v1/activate':
        activate($db, $body);
        break;
    case '/v1/validate':
        validate($db, $body);
        break;
    case '/v1/deactivate':
        deactivate($db, $body);
        break;
    default:
        json_error('NOT_FOUND', 'Unknown endpoint: ' . $path, 404);
}

/**
 * Activate a license key on a fingerprint.
 *
 * Seat semantics: a key can be active on at most `seats` distinct
 * fingerprints simultaneously. Activating again on the same fingerprint is
 * idempotent (re-validates, does not consume another seat).
 */
function activate(PDO $db, array $body): never
{
    $key = trim((string) ($body['license_key'] ?? ''));
    $fingerprint = trim((string) ($body['fingerprint'] ?? ''));
    if ($key === '' || $fingerprint === '') {
        json_error('VALIDATION_ERROR', 'license_key and fingerprint are required');
    }

    $stmt = $db->prepare('SELECT * FROM licenses WHERE key_hash = ?');
    $stmt->execute([key_hash($key)]);
    $license = $stmt->fetch(PDO::FETCH_ASSOC);
    if ($license === false) {
        json_out([
            'status' => 'invalid', 'message' => 'Invalid license key',
            'license_id' => '', 'plan' => '', 'seats' => 0, 'features' => [],
            'issued_to_name' => '', 'expires_at' => null,
        ], 402);
    }

    // Block revoked / suspended / expired keys.
    $now = time();
    if (($license['status'] ?? '') === 'revoked') {
        json_out(to_response($license, null), 402);
    }
    if (($license['status'] ?? '') === 'suspended') {
        json_out(to_response($license, null), 402);
    }
    if ($license['expires_at'] !== null && strtotime($license['expires_at']) < $now) {
        json_out(to_response($license, null), 402);
    }

    // Count current distinct activations.
    $countStmt = $db->prepare('SELECT COUNT(*) FROM activations WHERE license_id = ?');
    $countStmt->execute([$license['id']]);
    $used = (int) $countStmt->fetchColumn();

    // Already activated on this fingerprint -> idempotent re-validate.
    $existsStmt = $db->prepare('SELECT * FROM activations WHERE license_id = ? AND fingerprint = ?');
    $existsStmt->execute([$license['id'], $fingerprint]);
    $activation = $existsStmt->fetch(PDO::FETCH_ASSOC);
    if ($activation !== false) {
        $db->prepare('UPDATE activations SET last_seen_at = datetime(\'now\') WHERE license_id = ? AND fingerprint = ?')
            ->execute([$license['id'], $fingerprint]);
        json_out(to_response($license, $activation));
    }

    // Seat limit: do not allow more distinct fingerprints than seats.
    $seats = max(1, (int) $license['seats']);
    if ($used >= $seats) {
        json_out([
            'status' => 'suspended', 'message' => 'Activation limit reached for this license',
            'license_id' => $license['id'], 'plan' => $license['plan'], 'seats' => $seats,
            'features' => json_decode($license['features'] ?? '[]', true) ?: [],
            'issued_to_name' => $license['issued_to_name'], 'expires_at' => $license['expires_at'],
        ], 402);
    }

    $db->prepare('INSERT INTO activations (license_id, fingerprint) VALUES (?, ?)')
        ->execute([$license['id'], $fingerprint]);
    json_out(to_response($license, ['activated_at' => gmdate('Y-m-d\TH:i:s\Z', time())]));
}

function validate(PDO $db, array $body): never
{
    $fingerprint = trim((string) ($body['fingerprint'] ?? ''));
    if ($fingerprint === '') {
        json_error('VALIDATION_ERROR', 'fingerprint is required');
    }
    $resp = validate_fingerprint($db, $fingerprint);
    if ($resp === null) {
        json_out([
            'status' => 'invalid', 'message' => 'No active license for this installation',
            'license_id' => '', 'plan' => '', 'seats' => 0, 'features' => [],
            'issued_to_name' => '', 'expires_at' => null,
        ], 402);
    }
    json_out($resp);
}

function deactivate(PDO $db, array $body): never
{
    $licenseID = trim((string) ($body['license_id'] ?? ''));
    $fingerprint = trim((string) ($body['fingerprint'] ?? ''));
    if ($licenseID === '' || $fingerprint === '') {
        json_error('VALIDATION_ERROR', 'license_id and fingerprint are required');
    }
    $db->prepare('DELETE FROM activations WHERE license_id = ? AND fingerprint = ?')
        ->execute([$licenseID, $fingerprint]);
    json_out(['ok' => true]);
}
