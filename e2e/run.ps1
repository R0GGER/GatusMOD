# End-to-end test: NocoDB + Gatus + GatusMOD in docker compose.
# Usage: ./e2e/run.ps1 [-KeepRunning]
param([switch]$KeepRunning)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$run = Join-Path $PSScriptRoot '.run'
$mod = 'http://localhost:18080'
$noco = 'http://localhost:18081'
$adminPassword = 'e2e-password'
$failures = 0

function Check($name, [scriptblock]$condition) {
    $ok = $false
    try { $ok = [bool](& $condition) } catch { Write-Host "  error: $_" }
    if ($ok) { Write-Host "PASS  $name" -ForegroundColor Green }
    else { Write-Host "FAIL  $name" -ForegroundColor Red; $script:failures++ }
}

function WaitFor($name, [int]$seconds, [scriptblock]$condition) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        try { if (& $condition) { return $true } } catch { }
        Start-Sleep -Seconds 2
    }
    throw "Timed out waiting for $name"
}

function Get-Page($path, [hashtable]$headers = @{}) {
    $content = (Invoke-WebRequest -UseBasicParsing -Uri ($mod + $path) -Headers $headers).Content
    if ($content -is [byte[]]) { return [Text.Encoding]::UTF8.GetString($content) }
    $content
}

docker compose down -v --remove-orphans 2>$null | Out-Null
Remove-Item $run -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force "$run/config", "$run/images", "$run/public" | Out-Null
Set-Content "$run/images/favicon_512.png" 'png-512' -NoNewline
Set-Content "$run/public/favicon_256.png" 'png-256' -NoNewline

Write-Host '--- Starting NocoDB'
docker compose up -d nocodb | Out-Null
WaitFor 'NocoDB' 180 { (Invoke-WebRequest -UseBasicParsing "$noco/api/v1/health").StatusCode -eq 200 } | Out-Null

Write-Host '--- Creating NocoDB table and API token'
$signup = Invoke-RestMethod -Method Post "$noco/api/v1/auth/user/signup" -ContentType 'application/json' `
    -Body (@{ email = 'e2e@example.com'; password = 'E2e-Passw0rd!' } | ConvertTo-Json)
$auth = @{ 'xc-auth' = $signup.token }
$base = Invoke-RestMethod -Method Post "$noco/api/v2/meta/bases" -Headers $auth -ContentType 'application/json' -Body (@{ title = 'gatus' } | ConvertTo-Json)
$columns = @(
    @{ column_name = 'name'; title = 'name'; uidt = 'SingleLineText' },
    @{ column_name = 'group'; title = 'group'; uidt = 'SingleLineText' },
    @{ column_name = 'url'; title = 'url'; uidt = 'SingleLineText' },
    @{ column_name = 'method'; title = 'method'; uidt = 'SingleLineText' },
    @{ column_name = 'interval'; title = 'interval'; uidt = 'SingleLineText' },
    @{ column_name = 'conditions'; title = 'conditions'; uidt = 'LongText' },
    @{ column_name = 'disabled'; title = 'disabled'; uidt = 'Checkbox' },
    @{ column_name = 'extra'; title = 'extra'; uidt = 'LongText' }
)
$table = Invoke-RestMethod -Method Post "$noco/api/v2/meta/bases/$($base.id)/tables" -Headers $auth -ContentType 'application/json' `
    -Body (@{ table_name = 'endpoints'; title = 'endpoints'; columns = $columns } | ConvertTo-Json -Depth 5)
$apiToken = Invoke-RestMethod -Method Post "$noco/api/v1/tokens" -Headers $auth -ContentType 'application/json' -Body (@{ description = 'gatusmod' } | ConvertTo-Json)
$servers = Invoke-RestMethod -Method Post "$noco/api/v2/meta/bases/$($base.id)/tables" -Headers $auth -ContentType 'application/json' `
    -Body (@{ table_name = 'servers'; title = 'servers'; columns = @(@{ column_name = 'Host'; title = 'Host'; uidt = 'SingleLineText' }) } | ConvertTo-Json -Depth 5)
Invoke-RestMethod -Method Post "$noco/api/v2/tables/$($servers.id)/records" -Headers @{ 'xc-token' = $apiToken.token } -ContentType 'application/json' `
    -Body (ConvertTo-Json @(@{ Host = 'gatusmod' })) | Out-Null
# Checks GatusMOD runs itself. ${E2E_DB_PASSWORD} is expanded from the GatusMOD environment (single quotes: not by PowerShell).
$probeRows = @(
    @{ name = 'iperf'; group = 'probes'; url = 'iperf3://iperf3:5201?bandwidth=2s'; interval = '10s'; conditions = "[STATE] == ready`n[BANDWIDTH] > 10" },
    @{ name = 'postgres'; group = 'probes'; url = 'postgres://postgres:${E2E_DB_PASSWORD}@postgres:5432/postgres?sslmode=disable&query=SELECT%201'; interval = '10s'; conditions = '[RESULT] == 1' },
    @{ name = 'mariadb'; group = 'probes'; url = 'mysql://root:${E2E_DB_PASSWORD}@mariadb:3306/mysql?query=SELECT%20VERSION()'; interval = '10s'; conditions = '[RESULT] == pat(*MariaDB*)' },
    @{ name = 'redis'; group = 'probes'; url = 'redis://:${E2E_DB_PASSWORD}@redis:6379/0?query=PING'; interval = '10s'; conditions = '[RESULT] == PONG' },
    @{ name = 'redis-wrong-password'; group = 'probes'; url = 'redis://:wrong-password@redis:6379/0'; interval = '10s'; conditions = '[CONNECTED] == true' },
    @{ name = 'mongo'; group = 'probes'; url = 'mongodb://mongo:27017/admin?query=%7B%22ping%22%3A1%7D'; interval = '10s'; conditions = '[RESULT] == pat(*ok*)' },
    @{ name = 'smtp'; group = 'probes'; url = 'smtp://mailpit:1025'; interval = '10s'; conditions = "[CONNECTED] == true`n[CODE] == 220" },
    @{ name = 'backup'; group = 'probes'; url = 'push://'; interval = '1m' }
)
Invoke-RestMethod -Method Post "$noco/api/v2/tables/$($table.id)/records" -Headers @{ 'xc-token' = $apiToken.token } -ContentType 'application/json' `
    -Body (ConvertTo-Json -Depth 3 $probeRows) | Out-Null
Write-Host "  tables $($table.id), $($servers.id)"

Write-Host '--- Writing configuration'
Push-Location ..
$hash = (go run ./cmd/gatusmod hash-password $adminPassword)
Pop-Location
@"
skip-invalid-config-update: true
ui:
  dashboard-heading: E2E Heading
endpoints:
  - name: gatusmod
    group: e2e
    url: http://gatusmod:8080/mod/health
    interval: 10s
    conditions:
      - "[STATUS] == 200"
  - name: gatusmod-ping
    group: e2e
    url: icmp://gatusmod
    interval: 10s
    conditions:
      - "[CONNECTED] == true"
gatusmod:
  upstream: http://gatus:8080
  base-url: http://localhost:18080
  public-dir: /public
  defaults:
    interval: 15s
  ui:
    items-per-page: 24
    dashboard-heading-show: false
    footer: '<p>E2E footer</p>'
    favicon:
      apple-touch: "[baseurl]/favicon_256.png"
      size192x192: "[baseurl]/favicon_256.png"
      size512x512: "[baseurl]/images/favicon_512.png"
  analytics:
    provider: umami
    script-url: https://umami.example.com/script.js
    site-id: e2e-site
  database:
    provider: nocodb
    url: http://nocodb:8080
    token: $($apiToken.token)
    table-id: $($table.id)
    sync-interval: 10s
  admin:
    username: admin
    password-bcrypt: "$hash"
"@ | Set-Content "$run/config/config.yaml"

Write-Host '--- Starting Gatus and GatusMOD'
docker compose up -d --build gatus gatusmod iperf3 postgres mariadb redis mongo mailpit
if ($LASTEXITCODE -ne 0) { throw 'docker compose up failed' }
WaitFor 'GatusMOD' 120 { (Invoke-WebRequest -UseBasicParsing "$mod/mod/health").StatusCode -eq 200 } | Out-Null
WaitFor 'Gatus through GatusMOD' 120 { (Invoke-WebRequest -UseBasicParsing "$mod/api/v1/endpoints/statuses").StatusCode -eq 200 } | Out-Null

function Admin-Get($path) {
    $content = (Invoke-WebRequest -UseBasicParsing -Uri "$mod/mod/admin$path" -WebSession $script:session).Content
    if ($content -is [byte[]]) { return [Text.Encoding]::UTF8.GetString($content) }
    $content
}

function Admin-Post($path, [hashtable]$form) {
    $form.csrf = $script:csrf
    $content = (Invoke-WebRequest -UseBasicParsing -Method Post -Uri "$mod/mod/admin$path" -WebSession $script:session -Body $form).Content
    if ($content -is [byte[]]) { return [Text.Encoding]::UTF8.GetString($content) }
    $content
}

Write-Host '--- Logging in'
$loginPage = (Invoke-WebRequest -UseBasicParsing "$mod/mod/admin/" -SessionVariable session).Content
$loggedIn = (Invoke-WebRequest -UseBasicParsing -Method Post "$mod/mod/admin/login" -WebSession $session `
    -Body @{ username = 'admin'; password = $adminPassword; next = '/mod/admin/' }).Content
$csrf = [regex]::Match($loggedIn, 'name="csrf" value="([0-9a-f]+)"').Groups[1].Value

Write-Host '--- Importing an endpoint through the admin page'
$importYaml = @"
endpoints:
  - name: imported
    group: e2e
    url: http://gatusmod:8080/mod/health
    conditions:
      - "[STATUS] == 200"
    alerts: []
"@
$importPage = Admin-Post '/import' @{ target = 'yaml'; yaml = $importYaml }

Write-Host '--- Creating a database and a TCP table through the admin forms'
Admin-Post '/databases/save' @{ name = 'e2e-nocodb'; provider = 'nocodb'; url = 'http://nocodb:8080/api'; token = $apiToken.token } | Out-Null
$databaseId = [regex]::Match((Admin-Get '/databases'), '<strong>e2e-nocodb</strong>[\s\S]*?/mod/admin/databases/([0-9a-f]{12})/views/new').Groups[1].Value
$saved = Admin-Post "/databases/$databaseId/views/save" @{
    action = 'save'; name = 'servers'; group = 'e2e-servers'; tableId = $servers.id; type = 'tcp'; defaultPorts = '8080'; interval = '10s'; field_host = 'Host'
}
$serversId = [regex]::Match((Admin-Get '/databases'), "/mod/admin/databases/$databaseId/views/([0-9a-f]{12})/run").Groups[1].Value
$runResult = Admin-Post "/databases/$databaseId/views/$serversId/run" @{}

Write-Host '--- Changing the footer on the settings page'
$settingsPage = Admin-Get '/settings'
Admin-Post '/settings' @{
    baseUrl = 'http://localhost:18080'; defaultInterval = '15s'; itemsPerPage = '24'; subtitleShow = 'on'; footer = '<p>E2E admin footer</p>'
    favicon_apple = '[baseurl]/favicon_256.png'; favicon_192 = '[baseurl]/favicon_256.png'; favicon_512 = '[baseurl]/images/favicon_512.png'
    analyticsEnabled = 'on'; analyticsProvider = 'umami'; analyticsScript = 'https://umami.example.com/script.js'; analyticsSite = 'e2e-site'
} | Out-Null

Write-Host '--- Checks'
Check 'admin redirects to the login page without a session' { $loginPage -match 'name="password"' -and $loginPage -notmatch 'Add database' }
Check 'login gives a session with a CSRF token' { $csrf.Length -eq 64 }
Check 'post without CSRF token is rejected' {
    try { Invoke-WebRequest -UseBasicParsing -Method Post "$mod/mod/admin/sync" -WebSession $session | Out-Null; $false } catch { $_.Exception.Response.StatusCode.value__ -eq 403 }
}
Check 'legacy database block became a table of a database' { (Admin-Get '/databases') -match 'NocoDB:database' -and (Admin-Get '/databases') -match 'Cron @every 10s' }
Check 'import created the record in NocoDB' { $importPage -match 'Created: 1' }
Check 'TCP table was saved and run' { $databaseId -ne '' -and $serversId -ne '' -and $runResult -match '1 endpoints from 1 rows' }
Check 'settings page shows the YAML values' { $settingsPage -match 'E2E footer' }
$generated = "$run/config/zz-gatusmod-nocodb-database.yaml"
$generatedServers = "$run/config/zz-gatusmod-e2e-nocodb-servers.yaml"
Check 'sync wrote the file of the legacy table' { WaitFor 'generated file' 60 { (Test-Path $generated) -and ((Get-Content $generated -Raw) -match 'name: imported') } }
Check 'generated file uses defaults.interval' { (Get-Content $generated -Raw) -match 'interval: 15s' }
Check 'the TCP table has its own file' { (Get-Content $generatedServers -Raw) -match 'url: tcp://gatusmod:8080' -and (Get-Content $generatedServers -Raw) -match 'interval: 10s' }
Check 'Gatus reloaded and monitors the imported endpoint' {
    WaitFor 'Gatus reload' 120 { (Get-Page '/api/v1/endpoints/statuses') -match '"key":"e2e_imported"' }
}
$html = Get-Page '/'
Check 'dashboard HTML has GatusMOD assets' { $html -match '/mod/static/mod.js' -and $html -match 'window.gatusmod=\{"headingShow":false' }
Check 'dashboard HTML has the footer from the settings page' { $html -match '<footer id="gatusmod-footer"[^>]*><p>E2E admin footer</p></footer>' }
Check 'dashboard HTML has the favicons' { $html -match 'rel="apple-touch-icon"[^>]*favicon_256.png' -and $html -match 'sizes="512x512" href="http://localhost:18080/images/favicon_512.png"' }
Check 'dashboard HTML has analytics' { $html -match 'data-website-id="e2e-site"' }
Check 'endpoint detail page is patched too' { (Get-Page '/endpoints/e2e_imported') -match 'gatusmod-footer' }
Check 'related API finds the ICMP endpoint of the same host' {
    $related = Invoke-RestMethod "$mod/mod/api/endpoints/e2e_gatusmod/related"
    $related.type -eq 'http' -and $related.host -eq 'gatusmod' -and $related.icmp -eq 'e2e_gatusmod-ping'
}
Check 'related API has no ICMP endpoint for an ICMP endpoint' { $null -eq (Invoke-RestMethod "$mod/mod/api/endpoints/e2e_gatusmod-ping/related").icmp }
Check 'mod.js contains the statistics table' { (Get-Page '/mod/static/mod.js') -match 'applyStatistics' }
Check 'app.js items-per-page is patched' { (Get-Page '/js/app.js') -match '=24,[A-Za-z_$][\w$]*=\d+;var [A-Za-z_$][\w$]*=\{__name:"Home"' }
Check 'manifest has the 512 icon' { (Get-Page '/manifest.json') -match 'favicon_512.png' }
Check 'public-dir file served at the root' { (Get-Page '/favicon_256.png') -eq 'png-256' }
Check 'images-dir file served' { (Get-Page '/images/favicon_512.png') -eq 'png-512' }
$overview = Admin-Get '/'
Check 'admin overview shows sync and patch status' { $overview -match '10 valid endpoints' -and $overview -match 'items-per-page' -and $overview -notmatch 'class="error">items' }
$badgesPage = Admin-Get '/badges'
Check 'badge page lists the imported endpoint' { $badgesPage -match '/api/v1/endpoints/e2e_imported/health/badge.svg' }
Check 'badge page has the embed generator' { $badgesPage -match '/mod/badges/uptimes/30d/badge.svg' -and $badgesPage -match 'Import: servers' }
Check 'health badge is served by Gatus' { (Invoke-WebRequest -UseBasicParsing "$mod/api/v1/endpoints/e2e_imported/health/badge.svg").Headers['Content-Type'] -match 'svg' }
Check 'overall uptime badge is public' {
    WaitFor 'uptime data' 90 { (Get-Page '/mod/badges/uptimes/24h/badge.svg?content=percentage-endpoints') -match '\d%\D+\d+ endpoints' }
}

Write-Host '--- Probes (checks run by GatusMOD, reported as external endpoints)'
function Get-Status($key) { (Invoke-RestMethod "$mod/api/v1/endpoints/statuses?pageSize=100") | Where-Object { $_.key -eq $key } }
function Has-Result($key, [bool]$success) {
    $s = Get-Status $key
    $s -and @($s.results | Where-Object { $_.success -eq $success }).Count -gt 0
}
$generatedText = Get-Content $generated -Raw
Check 'generated file has external endpoints without URLs or passwords' {
    $generatedText -match 'external-endpoints:' -and $generatedText -match 'name: iperf' -and $generatedText -notmatch 'iperf3://' -and $generatedText -notmatch 'e2e-db-pass'
}
foreach ($key in 'probes_iperf', 'probes_postgres', 'probes_mariadb', 'probes_redis', 'probes_mongo', 'probes_smtp') {
    Check "$key reports up to Gatus" { WaitFor "$key result" 150 { Has-Result $key $true } }
}
Check 'iperf3 bandwidth was measured' { (Admin-Get '/') -match 'BANDWIDTH=\d+' }
Check 'probe results carry the response time' { @((Get-Status 'probes_postgres').results | Where-Object { $_.duration -gt 0 }).Count -gt 0 }
Check 'wrong password reports down without the password' {
    WaitFor 'redis down' 90 { Has-Result 'probes_redis-wrong-password' $false }
    $errors = ((Get-Status 'probes_redis-wrong-password').results | ForEach-Object { $_.errors }) -join ' '
    $errors -ne '' -and $errors -notmatch 'wrong-password'
}
Check 'related API knows the iperf3 endpoint' {
    $related = Invoke-RestMethod "$mod/mod/api/endpoints/probes_iperf/related"
    $related.type -eq 'iperf3' -and $related.host -eq 'iperf3'
}
$pushUrl = [regex]::Match((Admin-Get '/'), 'http://localhost:18080/mod/push/[0-9a-f]{32}').Value
Check 'overview shows the push URL' { $pushUrl -ne '' }
Check 'push URL reports down with the message' {
    WaitFor 'push accepted' 90 {
        try { (Invoke-WebRequest -UseBasicParsing "${pushUrl}?status=down&msg=e2e+push+failed").StatusCode -eq 200 } catch { $false }
    } | Out-Null
    WaitFor 'push result' 30 { ((Get-Status 'probes_backup').results | ForEach-Object { $_.errors }) -contains 'e2e push failed' }
}
Check 'unknown push token is rejected' {
    try { Invoke-WebRequest -UseBasicParsing "$mod/mod/push/0000" | Out-Null; $false } catch { $_.Exception.Response.StatusCode.value__ -eq 404 }
}

Write-Host '--- Status pages'
Admin-Post '/status-pages' @{
    slug = 'e2e-probes'; group = 'probes'; title = 'E2E Probes'; heading = 'Probe status'; domains = 'status.e2e.localhost'
} | Out-Null
# PowerShell does not let Invoke-WebRequest set the Host header, curl does.
function Curl-Status($hostName, $path) { & curl.exe -s -o NUL -w '%{http_code}' -H "Host: $hostName" ($mod + $path) }
function Curl-Body($hostName, $path) { (& curl.exe -s -H "Host: $hostName" ($mod + $path)) -join "`n" }
$statusPage = Get-Page '/status/e2e-probes'
Check 'status page on the main site is branded and scoped' { $statusPage -match '<title>E2E Probes</title>' -and $statusPage -match 'statuspage.js' -and $statusPage -match '"dashboardHeading":"Probe status"' }
Check 'status page header limits the statuses to its groups' {
    $keys = (Invoke-RestMethod "$mod/api/v1/endpoints/statuses" -Headers @{ 'X-GatusMOD-Status-Page' = 'e2e-probes' }).key
    @($keys).Count -gt 0 -and @($keys | Where-Object { $_ -notlike 'probes_*' }).Count -eq 0
}
Check 'main site still shows every endpoint' { @((Invoke-RestMethod "$mod/api/v1/endpoints/statuses").key | Where-Object { $_ -like 'e2e_*' }).Count -gt 0 }
Check 'domain of a status page only shows its groups' {
    $keys = (Curl-Body 'status.e2e.localhost' '/api/v1/endpoints/statuses' | ConvertFrom-Json).key
    @($keys).Count -gt 0 -and @($keys | Where-Object { $_ -notlike 'probes_*' }).Count -eq 0
}
Check 'domain of a status page is branded without the scope script' {
    $page = Curl-Body 'status.e2e.localhost' '/'
    $page -match '<title>E2E Probes</title>' -and $page -notmatch 'statuspage.js'
}
Check 'domain of a status page hides other endpoints, admin and badges' {
    (Curl-Status 'status.e2e.localhost' '/api/v1/endpoints/e2e_gatusmod/statuses') -eq '404' -and
    (Curl-Status 'status.e2e.localhost' '/api/v1/endpoints/probes_postgres/statuses') -eq '200' -and
    (Curl-Status 'status.e2e.localhost' '/mod/admin/') -eq '404' -and
    (Curl-Status 'status.e2e.localhost' '/mod/badges/uptimes/24h/badge.svg') -eq '404'
}

Write-Host '--- GatusMOD log'
docker compose logs gatusmod --no-log-prefix | Select-Object -Last 25

if (-not $KeepRunning) { docker compose down -v | Out-Null }
if ($failures -gt 0) { Write-Host "$failures check(s) failed" -ForegroundColor Red; exit 1 }
Write-Host 'All checks passed' -ForegroundColor Green
