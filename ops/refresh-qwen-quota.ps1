param(
    [string]$QuotaExecutable = 'D:\WILL\AGENT\CPA\qwen-cliproxyapi\bin\bailian-quota.exe',
    [string]$BlPath = 'C:\ProgramData\bailian-cli\bl.cmd',
    [string]$CacheFile = 'C:\ProgramData\cpa-qwen-quota\qwen-quota.json',
    [string]$StateFile = 'C:\ProgramData\cpa-qwen-quota\login-prompted.state',
    [switch]$SelfTest
)

$ErrorActionPreference = 'Stop'
$ConsoleUrl = 'https://bailian.console.aliyun.com/cn-beijing/?tab=model#/subscription/token-plan/personal'

function Get-QuotaErrorCode([string]$Output) {
    try {
        $value = $Output | ConvertFrom-Json -ErrorAction Stop
        if ($value.error -is [string]) { return [string]$value.error }
    } catch {}
    return ''
}

function Update-LoginPromptState([string]$Code, [string]$Path, [scriptblock]$Notify) {
    if ($Code -eq 'not_logged_in') {
        if (-not (Test-Path -LiteralPath $Path)) {
            $directory = Split-Path -Parent $Path
            if ($directory) { [IO.Directory]::CreateDirectory($directory) | Out-Null }
            [IO.File]::WriteAllText($Path, [DateTime]::UtcNow.ToString('o'))
            & $Notify
            return 'notified'
        }
        return 'suppressed'
    }
    if ($Code -eq '') {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
        return 'reset'
    }
    return 'ignored'
}

function Show-LoginPrompt {
    # The notice must not block the refresh task: a scheduled instance that waits
    # for a click blocks every later trigger (MultipleInstancesPolicy=IgnoreNew).
    $notice = Join-Path (Split-Path -Parent $StateFile) 'login-notice.ps1'
    $lines = @(
        'Add-Type -AssemblyName System.Windows.Forms',
        "[void][System.Windows.Forms.MessageBox]::Show('百炼控制台登录已失效。请在浏览器中登录对应的阿里云账号，登录成功后 10 分钟内自动恢复额度刷新。','Qwen 额度需要重新登录',0,64)"
    )
    # Windows PowerShell reads a UTF-8 file without a BOM as ANSI, which breaks
    # the Chinese literal above; the notice is therefore always written with BOM.
    [IO.File]::WriteAllText($notice, ($lines -join "`r`n"), (New-Object System.Text.UTF8Encoding($true)))
    Start-Process $ConsoleUrl
    Start-Process 'powershell.exe' -WindowStyle Hidden -ArgumentList '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $notice
}

if ($SelfTest) {
    $root = Join-Path ([IO.Path]::GetTempPath()) ('qwen-login-notify-' + [guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($root) | Out-Null
    $state = Join-Path $root 'state'
    $calls = [System.Collections.Generic.List[int]]::new()
    $notify = { $calls.Add(1) }.GetNewClosure()
    $auth = (@{ error = 'not_logged_in'; message = 'safe' } | ConvertTo-Json -Compress)
    $other = (@{ error = 'invalid_response'; message = 'safe' } | ConvertTo-Json -Compress)
    $checks = @(
        ((Get-QuotaErrorCode $auth) -eq 'not_logged_in'),
        ((Update-LoginPromptState 'not_logged_in' $state $notify) -eq 'notified'),
        ((Update-LoginPromptState 'not_logged_in' $state $notify) -eq 'suppressed'),
        ($calls.Count -eq 1),
        ((Update-LoginPromptState (Get-QuotaErrorCode $other) $state $notify) -eq 'ignored'),
        ($calls.Count -eq 1),
        ((Update-LoginPromptState '' $state $notify) -eq 'reset'),
        (-not (Test-Path $state)),
        ((Update-LoginPromptState 'not_logged_in' $state $notify) -eq 'notified'),
        ($calls.Count -eq 2)
    )
    Remove-Item $root -Recurse -Force
    if ($checks -contains $false) { throw 'login notification self-test failed' }
    Write-Output 'login notification self-test passed'
    exit 0
}

$directory=Split-Path -Parent $StateFile
$stdoutFile=Join-Path $directory 'refresh.out.log'
$stderrFile=Join-Path $directory 'refresh.err.log'
$startInfo=New-Object Diagnostics.ProcessStartInfo
$startInfo.FileName=$QuotaExecutable
$startInfo.UseShellExecute=$false
$startInfo.CreateNoWindow=$true
$startInfo.RedirectStandardOutput=$true
$startInfo.RedirectStandardError=$true
$startInfo.Arguments='--json --source bl --bl-path "'+$BlPath+'" --timeout 120s --cache-file "'+$CacheFile+'"'
$process=New-Object Diagnostics.Process
$process.StartInfo=$startInfo
if(-not $process.Start()){throw 'quota CLI failed to start'}
$stdoutTask=$process.StandardOutput.ReadToEndAsync()
$stderrTask=$process.StandardError.ReadToEndAsync()
$process.WaitForExit()
$stdout=$stdoutTask.GetAwaiter().GetResult()
$stderr=$stderrTask.GetAwaiter().GetResult()
[IO.File]::WriteAllText($stdoutFile,$stdout,[Text.Encoding]::UTF8)
[IO.File]::WriteAllText($stderrFile,$stderr,[Text.Encoding]::UTF8)
$code=if($process.ExitCode -eq 0){''}else{Get-QuotaErrorCode $stdout}
if($process.ExitCode -eq 0){[void](Update-LoginPromptState '' $StateFile {})}
elseif($code -eq 'not_logged_in'){[void](Update-LoginPromptState $code $StateFile {Show-LoginPrompt})}
exit $process.ExitCode