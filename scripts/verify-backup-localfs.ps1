# Design experiment only: synthetic plaintext under a unique workspace directory.
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$PSDefaultParameterValues['*:Encoding'] = 'utf8'
$ErrorActionPreference = 'Stop'
$workspace = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$runId = [Guid]::NewGuid().ToString('N')
$testRoot = Join-Path $workspace ('.toolchain\backup-localfs-' + $runId)
$checks = New-Object System.Collections.Generic.List[object]
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
$report = @{ run_id = $runId; platform = 'windows'; powershell = $PSVersionTable.PSVersion.ToString(); checks = $checks; cleanup = $false }

function Check([string]$name, [bool]$passed) {
    $checks.Add(@{ name = $name; passed = $passed })
    if (-not $passed) { throw "Check failed: $name" }
    Write-Output "PASS $name"
}

function PrivateAcl {
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $rule = New-Object Security.AccessControl.FileSystemAccessRule(
        $sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl.AddAccessRule($rule)
    return $acl
}

function IsPrivate([string]$path) {
    $acl = Get-Acl -LiteralPath $path
    if ($acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -ne $sid.Value) { return $false }
    $rules = $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])
    $ownerAllowed = $false
    foreach ($rule in $rules) {
        if ($rule.AccessControlType -eq 'Allow') {
            if ($rule.IdentityReference.Value -ne $sid.Value) { return $false }
            $ownerAllowed = $true
        }
    }
    return $ownerAllowed
}

function NewPrivateDirectory([string]$path, [Security.AccessControl.DirectorySecurity]$explicitAcl = $null) {
    $acl = $explicitAcl
    if ($null -eq $acl) { $acl = PrivateAcl }
    if ($PSVersionTable.PSVersion.Major -ge 6) {
        [IO.FileSystemAclExtensions]::CreateDirectory($acl, $path) | Out-Null
    } else {
        [IO.Directory]::CreateDirectory($path, $acl) | Out-Null
    }
}

function NewPrivateFile([string]$path) {
    $acl = New-Object Security.AccessControl.FileSecurity
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'Allow')))
    if ($PSVersionTable.PSVersion.Major -ge 6) {
        return [IO.FileSystemAclExtensions]::Create((New-Object IO.FileInfo($path)), [IO.FileMode]::CreateNew,
            [Security.AccessControl.FileSystemRights]::FullControl, [IO.FileShare]::None,
            4096, [IO.FileOptions]::None, $acl)
    }
    return New-Object IO.FileStream($path, [IO.FileMode]::CreateNew,
        [Security.AccessControl.FileSystemRights]::FullControl, [IO.FileShare]::None,
        4096, [IO.FileOptions]::None, $acl)
}

function NoReparseComponents([string]$path) {
    $item = Get-Item -LiteralPath $path -Force
    while ($null -ne $item) {
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { return $false }
        if ($item -is [IO.FileInfo]) { $item = $item.Directory } else { $item = $item.Parent }
    }
    return $true
}

try {
    # The DirectorySecurity overload applies the DACL when creating the directory.
    NewPrivateDirectory $testRoot
    $private = Join-Path $testRoot 'private'
    NewPrivateDirectory $private
    Check 'new directory has protected owner-only DACL' ((IsPrivate $private) -and (Get-Acl -LiteralPath $private).AreAccessRulesProtected)
    Check 'normal ancestry contains no reparse components' (NoReparseComponents $private)
    $plain = Join-Path $private 'synthetic.txt'
    [IO.File]::WriteAllText($plain, 'synthetic plaintext only', (New-Object Text.UTF8Encoding($false)))
    $fileAcl = Get-Acl -LiteralPath $plain
    $report.file_owner = $fileAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    $report.file_sddl = $fileAcl.Sddl
    # Default owner can be Administrators even when the inherited DACL is private.
    # Recreate an empty file with an explicit owner and DACL before writing data.
    Remove-Item -LiteralPath $plain
    $stream = NewPrivateFile $plain
    try {
        Check 'explicit file owner and DACL verified before plaintext write' (IsPrivate $plain)
        $bytes = [Text.Encoding]::UTF8.GetBytes('synthetic plaintext only')
        $stream.Write($bytes, 0, $bytes.Length)
        $stream.Flush($true)
    } finally { $stream.Dispose() }
    $exclusive = $false
    try { $other = NewPrivateFile $plain; $other.Dispose() } catch { $exclusive = $true }
    Check 'CreateNew refuses an existing plaintext file' $exclusive
    Check 'synthetic plaintext roundtrip' ([IO.File]::ReadAllText($plain) -eq 'synthetic plaintext only')

    $permissive = Join-Path $testRoot 'permissive'
    $acl = PrivateAcl
    $others = New-Object Security.Principal.SecurityIdentifier('S-1-5-32-545')
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($others, 'ReadAndExecute', 'Allow')))
    NewPrivateDirectory $permissive $acl
    Check 'directory with Users read permission is rejected' (-not (IsPrivate $permissive))

    $alias = Join-Path $testRoot 'alias'
    New-Item -ItemType Junction -Path $alias -Target $private | Out-Null
    Check 'junction alias is rejected before plaintext use' (-not (NoReparseComponents $alias))
    Check 'junction child is rejected through ancestor walk' (-not (NoReparseComponents (Join-Path $alias 'synthetic.txt')))
    Check 'junction is a real alias of synthetic data' ([IO.File]::ReadAllText((Join-Path $alias 'synthetic.txt')) -eq 'synthetic plaintext only')
    # Remove only the junction, without traversing or recursively deleting it.
    [IO.Directory]::Delete($alias)
    Check 'removing alias leaves target data intact' ([IO.File]::Exists($plain))
    $hard = Join-Path $private 'hard.txt'
    New-Item -ItemType HardLink -Path $hard -Target $plain | Out-Null
    $linkCount = & python -c 'import os,sys; print(os.stat(sys.argv[1]).st_nlink)' $plain
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect link count' }
    Check 'hard-linked plaintext detected by link count' ([int]$linkCount -eq 2)
    Remove-Item -LiteralPath $hard
    Check 'hard-link removal preserves plaintext target' ([IO.File]::ReadAllText($plain) -eq 'synthetic plaintext only')
    $report.passed = $true
} catch {
    $report.passed = $false
    $report.failure = $_.Exception.Message
    throw
} finally {
    $resolved = [IO.Path]::GetFullPath($testRoot)
    $expected = Join-Path $workspace ('.toolchain\backup-localfs-' + $runId)
    if (-not (Test-Path -LiteralPath $resolved)) { $report.cleanup = $true }
    if ($resolved -eq $expected -and (Test-Path -LiteralPath $resolved)) {
        # If a failure left a junction behind, delete that link before recursion.
        $aliasPath = Join-Path $resolved 'alias'
        if (Test-Path -LiteralPath $aliasPath) {
            $aliasItem = Get-Item -LiteralPath $aliasPath -Force
            if (($aliasItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { [IO.Directory]::Delete($aliasPath) }
        }
        if (-not (NoReparseComponents $resolved)) { throw 'Refusing cleanup of aliased root' }
        Remove-Item -LiteralPath $resolved -Recurse -Force
        $report.cleanup = -not (Test-Path -LiteralPath $resolved)
    }
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $workspace '.toolchain\backup-localfs-windows.json') -Encoding UTF8
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $workspace ('.toolchain\backup-localfs-windows-' + $runId + '.json')) -Encoding UTF8
    if (-not $report.cleanup) { throw 'Prototype cleanup did not complete' }
}
