#requires -version 5.1
<#
.SYNOPSIS
  Bootstrap the OpenMeter/Konnect metering catalog for the clearinghouse collector.

.DESCRIPTION
  Idempotent: creates only what is missing. Meters are immutable in OpenMeter, so this
  script never updates or deletes them. Uses `kongctl api` against /v3/openmeter/*.

  Auth:     $env:KONGCTL_DEFAULT_KONNECT_PAT (preferred) or $env:OPENMETER_API_KEY (Konnect kpat_…).
  Endpoint: $env:OPENMETER_URL (default https://us.api.konghq.com/v3/openmeter).

.EXAMPLE
  .\bootstrap.ps1 catalog
.EXAMPLE
  .\bootstrap.ps1 customer demo-client demo-user "Demo User"
.EXAMPLE
  .\bootstrap.ps1 all demo-client demo-user "Demo User" -Subscribe
#>
[CmdletBinding()]
param(
  [ValidateSet('catalog', 'customer', 'all')]
  [string]$Command = 'catalog',
  [string]$ClientId,
  [string]$ExternalUserId,
  [string]$DisplayName,
  [switch]$Subscribe
)

$ErrorActionPreference = 'Stop'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$Catalog = if ($env:CATALOG) { $env:CATALOG } else { Join-Path $ScriptDir 'catalog.json' }

function Die($m) { Write-Error $m; exit 1 }
function Info($m) { Write-Host $m }
function Warn($m) { Write-Warning $m }

if (-not (Get-Command kongctl -ErrorAction SilentlyContinue)) { Die 'kongctl not found (https://developer.konghq.com/kongctl/)' }
if (-not (Test-Path $Catalog)) { Die "catalog not found: $Catalog" }

# --- env file (repo-root .env) ---------------------------------------------
$EnvFile = if ($env:ENV_FILE) { $env:ENV_FILE } else { Join-Path (Split-Path (Split-Path $ScriptDir -Parent) -Parent) '.env' }
if (Test-Path $EnvFile) {
  Get-Content $EnvFile | ForEach-Object {
    if ($_ -match '^\s*#' -or $_ -match '^\s*$') { return }
    if ($_ -match '^([^=]+)=(.*)$') {
      $k = $Matches[1].Trim()
      $v = $Matches[2].Trim()
      if (($v.StartsWith('"') -and $v.EndsWith('"')) -or ($v.StartsWith("'") -and $v.EndsWith("'"))) {
        $v = $v.Substring(1, $v.Length - 2)
      }
      if (-not (Test-Path "Env:$k")) { Set-Item -Path "Env:$k" -Value $v }
    }
  }
}

# --- auth + endpoint -------------------------------------------------------
$pat = if ($env:KONGCTL_DEFAULT_KONNECT_PAT) { $env:KONGCTL_DEFAULT_KONNECT_PAT } else { $env:OPENMETER_API_KEY }
if (-not $pat) { Die "no Konnect PAT — set KONGCTL_DEFAULT_KONNECT_PAT or OPENMETER_API_KEY in the environment or in $EnvFile" }
$env:KONGCTL_DEFAULT_KONNECT_PAT = $pat

if (-not $env:OPENMETER_URL -and $env:OPENMETER_INGEST_URL) {
  $env:OPENMETER_URL = ($env:OPENMETER_INGEST_URL.TrimEnd('/') -replace '/events/?$', '')
}
$omUrl = if ($env:OPENMETER_URL) { $env:OPENMETER_URL } else { 'https://us.api.konghq.com/v3/openmeter' }
$omUrl = $omUrl.TrimEnd('/')
$BASE = [regex]::Replace($omUrl, '(https?://[^/]+).*', '$1')
$PREFIX = [regex]::Replace($omUrl, 'https?://[^/]+', '')
if (-not $PREFIX) { $PREFIX = '/v3/openmeter' }

$catalogObj = Get-Content -Raw $Catalog | ConvertFrom-Json

# --- kongctl api helpers ---------------------------------------------------
function Kapi-Err($method, $path, $detail) {
  Die "kongctl api $method $path failed — check OPENMETER_URL ($omUrl) and your PAT: $detail"
}
function Kapi-Warn($method, $path, $detail) {
  Warn "kongctl api $method $path failed — check OPENMETER_URL ($omUrl) and your PAT: $detail"
}
function Kapi-BodyError($obj) {
  if ($obj.message) { return $obj.message }
  if ($obj.detail) { return $obj.detail }
  return $obj.title
}
function Kapi-BodyIsError($obj) {
  if ($null -eq $obj) { return $true }
  $msg = Kapi-BodyError $obj
  if (-not $msg) { return $false }
  $hasData = $obj.PSObject.Properties.Name -contains 'data' -or $obj.PSObject.Properties.Name -contains 'items'
  return -not $hasData
}
function Kapi-Run($soft, $method, $path, $bodyJson) {
  $errFile = [System.IO.Path]::GetTempFileName()
  try {
    $out = if ($method -eq 'delete') {
      & kongctl api delete "$PREFIX$path" --base-url $BASE -o json 2>$errFile
    }
    elseif ($method -eq 'get') {
      & kongctl api get "$PREFIX$path" --base-url $BASE -o json 2>$errFile
    }
    else {
      $bodyJson | & kongctl api $method "$PREFIX$path" --base-url $BASE -o json -f - 2>$errFile
    }
    if ($LASTEXITCODE -ne 0) {
      if ($soft) { Kapi-Warn $method $path (Get-Content -Raw $errFile); return $null }
      Kapi-Err $method $path (Get-Content -Raw $errFile)
    }
    if (-not $out) {
      if ($soft) { Kapi-Warn $method $path 'empty response'; return $null }
      Kapi-Err $method $path 'empty response'
    }
    $obj = $out | ConvertFrom-Json
    if (Kapi-BodyIsError $obj) {
      if ($soft) { Kapi-Warn $method $path (Kapi-BodyError $obj); return $null }
      Kapi-Err $method $path (Kapi-BodyError $obj)
    }
    return $obj
  }
  finally { Remove-Item -Force $errFile -ErrorAction SilentlyContinue }
}
function Kapi-Get($path) { return Kapi-Run $false get $path $null }
function Kapi-Send($method, $path, $bodyJson) { return Kapi-Run $false $method $path $bodyJson }
function Kapi-Delete($path) { Kapi-Run $false delete $path $null | Out-Null }
function Kapi-Get-Soft($path) { return Kapi-Run $true get $path $null }
function Kapi-Send-Soft($method, $path, $bodyJson) { return Kapi-Run $true $method $path $bodyJson }
function Kapi-Delete-Soft($path) { Kapi-Run $true delete $path $null | Out-Null }
function Plan-ConfigKey {
  if ($catalogObj.plan -and $catalogObj.plan.key) { return $catalogObj.plan.key }
  return $catalogObj.plan_key
}
function Meter-IdFor($meterKey) {
  $m = Items (Kapi-Get '/meters') | Where-Object { $_.key -eq $meterKey } | Select-Object -First 1
  if (-not $m) { return $null }
  return $m.id
}
function Feature-For($featureKey) {
  return Items (Kapi-Get '/features') | Where-Object { $_.key -eq $featureKey } | Select-Object -First 1
}
function Find-PlanByStatus($planKey, $status) {
  return Items (Kapi-Get "/plans?filter[key]=$planKey&filter[status]=$status") |
    Where-Object { $_.key -eq $planKey } | Select-Object -First 1
}
function Feature-MeterKey($feature) {
  if ($feature.meter_key) { return $feature.meter_key }
  return $feature.meter_slug
}
function New-FeatureBody($key, $name, $meterId) {
  return (@{ key = $key; name = $name; meter = @{ id = $meterId } } | ConvertTo-Json -Compress)
}
function Items($resp) {
  if ($null -eq $resp) { return @() }
  if ($resp.PSObject.Properties.Name -contains 'data') { return @($resp.data) }
  return @($resp)
}

# --- catalog ---------------------------------------------------------------
function Ensure-Meters {
  $existing = (Items (Kapi-Get '/meters')).key
  foreach ($m in $catalogObj.meters) {
    if ($existing -contains $m.key) { Info "meter   $($m.key) - exists"; continue }
    $body = [ordered]@{ name = $m.name; key = $m.key; description = $m.description; event_type = $m.event_type; aggregation = $m.aggregation }
    if ($m.value_property) { $body.value_property = $m.value_property }
    if ($m.dimensions) { $body.dimensions = $m.dimensions }
    Kapi-Send 'post' '/meters' ($body | ConvertTo-Json -Depth 6 -Compress) | Out-Null
    Info "meter   $($m.key) - created"
  }
}
function Ensure-Features {
  foreach ($f in $catalogObj.features) {
    $meterKey = Feature-MeterKey $f
    if (-not $meterKey) { Die "feature $($f.key) requires meter_key in catalog.json" }
    $meterId = Meter-IdFor $meterKey
    if (-not $meterId) { Die "meter $meterKey not found for feature $($f.key)" }

    $feat = Feature-For $f.key
    if (-not $feat) {
      Kapi-Send 'post' '/features' (New-FeatureBody $f.key $f.name $meterId) | Out-Null
      Info "feature $($f.key) - created"
      continue
    }

    $linked = $feat.meter.id
    if ($linked -eq $meterId) { Info "feature $($f.key) - exists"; continue }

    Warn "feature $($f.key) - exists without meter link; recreating"
    Kapi-Delete-Soft "/features/$($feat.id)"
    Kapi-Send 'post' '/features' (New-FeatureBody $f.key $f.name $meterId) | Out-Null
    Info "feature $($f.key) - recreated (meter: $meterKey)"
  }
}
function Build-PlanBody {
  $featMap = @{}
  foreach ($feat in Items (Kapi-Get '/features')) { $featMap[$feat.key] = $feat.id }
  $p = $catalogObj.plan
  $phases = @()
  foreach ($phase in $p.phases) {
    $rateCards = @()
    foreach ($rc in $phase.rate_cards) {
      $rateCards += [ordered]@{
        key = $rc.key
        name = $rc.name
        feature = @{ id = $featMap[$rc.feature_key] }
        billing_cadence = $rc.billing_cadence
        price = $rc.price
      }
    }
    $phases += [ordered]@{ key = $phase.key; name = $phase.name; rate_cards = $rateCards }
  }
  $body = [ordered]@{
    key = $p.key
    name = $p.name
    currency = $p.currency
    billing_cadence = $p.billing_cadence
    phases = $phases
  }
  if ($p.description) { $body.description = $p.description }
  return ($body | ConvertTo-Json -Depth 8 -Compress)
}
function Publish-Plan($planId, $planKey) {
  $published = Kapi-Send-Soft 'post' "/plans/$planId/publish" '{}'
  if ($published -and $published.status -eq 'active') {
    Info "plan    $planKey - published"
    return $true
  }
  Warn "plan    $planKey - could not publish (ensure features have meter links)"
  return $false
}
function Ensure-Plan {
  $planKey = Plan-ConfigKey
  if (-not $planKey) { Info 'plan    - none configured'; return }
  if (-not $catalogObj.plan) { Die 'catalog plan block missing - add .plan or remove plan_key' }

  if (Find-PlanByStatus $planKey 'active') { Info "plan    $planKey - active"; return }

  $draft = Find-PlanByStatus $planKey 'draft'
  if ($draft) {
    Info "plan    $planKey - draft exists, publishing"
    Publish-Plan $draft.id $planKey | Out-Null
    return
  }

  $bodyJson = Build-PlanBody
  $bodyObj = $bodyJson | ConvertFrom-Json
  foreach ($phase in $bodyObj.phases) {
    foreach ($rc in $phase.rate_cards) {
      if (-not $rc.feature.id) { Die 'plan rate cards reference unknown features - run ensure_features first' }
    }
  }
  $created = Kapi-Send 'post' '/plans' $bodyJson
  Info "plan    $planKey - created (draft)"
  Publish-Plan $created.id $planKey | Out-Null
}
function Invoke-Catalog {
  Info "== catalog ($BASE$PREFIX) =="
  Ensure-Meters; Ensure-Features; Ensure-Plan
}

# --- customer --------------------------------------------------------------
function Ensure-Customer($clientId, $externalUserId, $display, $subscribe) {
  if (-not $clientId -or -not $externalUserId) { Die 'customer requires <client_id> <external_user_id>' }
  # CloudEvent subject = compound client_id:external_user_id = customer key = its only
  # subject_key. OpenMeter forbids changing subject_keys on subscribed customers, so we
  # set it at creation and never mutate it.
  $compound = "$clientId`:$externalUserId"
  if (-not $display) { $display = $compound }

  $cust = Items (Kapi-Get '/customers') | Where-Object { $_.key -eq $compound } | Select-Object -First 1
  if (-not $cust) {
    $body = [ordered]@{ key = $compound; name = $display; usage_attribution = @{ subject_keys = @($compound) } }
    $created = Kapi-Send 'post' '/customers' ($body | ConvertTo-Json -Depth 5 -Compress)
    $id = $created.id
    Info "customer $compound - created (subject: $compound)"
  }
  else {
    $id = $cust.id
    if ($cust.usage_attribution.subject_keys -contains $compound) { Info "customer $compound - up to date" }
    else {
      Warn "customer $compound exists but its subject_keys do not include '$compound'"
      Warn "  (OpenMeter blocks subject_key changes on subscribed customers - reconcile manually)"
    }
  }
  if ($subscribe) { Ensure-Subscription $id $compound }
}

function Ensure-Subscription($customerId, $label) {
  $planKey = Plan-ConfigKey
  if (-not $planKey) { Warn 'no plan_key in catalog; skipping subscription'; return }
  $existing = Items (Kapi-Get-Soft "/subscriptions?customer_id=$customerId") | Where-Object { $_.customer_id -eq $customerId }
  if ($existing) { Info "sub      $label - exists"; return }
  $body = [ordered]@{
    customer = [ordered]@{ key = $label }
    plan     = [ordered]@{ key = $planKey }
  }
  $created = Kapi-Send-Soft 'post' '/subscriptions' ($body | ConvertTo-Json -Compress)
  if ($created) {
    Info "sub      $label - created on $planKey"
  }
  else {
    Warn "sub      $label - could not create subscription on $planKey (create manually if needed)"
  }
}

# --- dispatch --------------------------------------------------------------
switch ($Command) {
  'catalog' { Invoke-Catalog }
  'customer' { Ensure-Customer $ClientId $ExternalUserId $DisplayName $Subscribe.IsPresent }
  'all' { Invoke-Catalog; Ensure-Customer $ClientId $ExternalUserId $DisplayName $Subscribe.IsPresent }
}
Info 'done.'
