param(
    [Parameter(Mandatory=$true)][string]$QuotaExecutable,
    [string]$BlPath = 'C:\ProgramData\bailian-cli\bl.cmd',
    [Parameter(Mandatory=$true)][string]$CacheFile,
    [Parameter(Mandatory=$true)][string]$StateFile,
    [int]$RecoveryTimeoutSeconds = 90,
    [switch]$SelfTest
)
$ErrorActionPreference='Stop'
function Get-QuotaErrorCode([string]$Output){try{$value=$Output|ConvertFrom-Json -ErrorAction Stop;if($value.error -is [string]){return [string]$value.error}}catch{};return ''}
function Update-LoginPromptState([string]$Code,[string]$Path,[scriptblock]$Notify){if($Code -eq 'not_logged_in'){if(-not(Test-Path -LiteralPath $Path)){$directory=Split-Path -Parent $Path;if($directory){[IO.Directory]::CreateDirectory($directory)|Out-Null};[IO.File]::WriteAllText($Path,[DateTime]::UtcNow.ToString('o'));&$Notify;return 'notified'};return 'suppressed'};if($Code -eq ''){Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue;return 'reset'};return 'ignored'}

# The quota CLI always runs with the same fixed arguments; stdout/stderr keep
# their historical log names so existing operational notes stay valid.
function Invoke-QuotaRefresh {
    $directory=Split-Path -Parent $StateFile
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
    [IO.File]::WriteAllText((Join-Path $directory 'refresh.out.log'),$stdout,[Text.Encoding]::UTF8)
    [IO.File]::WriteAllText((Join-Path $directory 'refresh.err.log'),$stderr,[Text.Encoding]::UTF8)
    return New-Object psobject -Property @{ExitCode=$process.ExitCode; Output=$stdout}
}

# Console quota needs the official CLI's own browser sign-in: it opens the login
# page and stores the console token in the refresh user's profile.
function Start-LoginFlow {
    if(-not (Test-Path -LiteralPath $BlPath)){return $false}
    try{Start-Process -FilePath $BlPath -ArgumentList 'auth','login','--console' -WindowStyle Hidden;return $true}
    catch{return $false}
}

# The notice must not block the refresh task: a scheduled instance that waits
# for a click blocks every later trigger (MultipleInstancesPolicy=IgnoreNew).
function Show-LoginPrompt {
    $notice=Join-Path (Split-Path -Parent $StateFile) 'login-notice.ps1'
    $lines=@(
        'Add-Type -AssemblyName System.Windows.Forms',
        "[void][System.Windows.Forms.MessageBox]::Show('百炼控制台登录已过期。已为你打开登录页面，请在浏览器中完成登录；登录成功后 10 分钟内自动恢复额度刷新。','Qwen 额度需要重新登录',0,64)"
    )
    # Windows PowerShell reads a UTF-8 file without a BOM as ANSI, which breaks
    # the Chinese literal above; the notice is therefore always written with BOM.
    [IO.File]::WriteAllText($notice,($lines -join "`r`n"),(New-Object System.Text.UTF8Encoding($true)))
    Start-Process 'powershell.exe' -WindowStyle Hidden -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File',$notice
}

if($SelfTest){
    $root=Join-Path $env:LOCALAPPDATA ('qwen-login-notify-'+[guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($root)|Out-Null
    try{
        $state=Join-Path $root 'state'
        $calls=[System.Collections.Generic.List[int]]::new()
        $notify={$calls.Add(1)}.GetNewClosure()
        $auth=(@{error='not_logged_in'}|ConvertTo-Json -Compress)
        $other=(@{error='invalid_response'}|ConvertTo-Json -Compress)
        $checks=@(
            ((Get-QuotaErrorCode $auth)-eq 'not_logged_in'),
            ((Update-LoginPromptState 'not_logged_in' $state $notify)-eq 'notified'),
            ((Update-LoginPromptState 'not_logged_in' $state $notify)-eq 'suppressed'),
            ($calls.Count -eq 1),
            ((Update-LoginPromptState (Get-QuotaErrorCode $other) $state $notify)-eq 'ignored'),
            ($calls.Count -eq 1),
            ((Update-LoginPromptState '' $state $notify)-eq 'reset'),
            (-not(Test-Path $state)),
            ((Update-LoginPromptState 'not_logged_in' $state $notify)-eq 'notified'),
            ($calls.Count -eq 2)
        )
        if($checks -contains $false){throw 'login notification self-test failed'}
        Write-Output 'login notification self-test passed'
    } finally {Remove-Item $root -Recurse -Force -ErrorAction SilentlyContinue}
    exit 0
}

$result=Invoke-QuotaRefresh
if($result.ExitCode -eq 0){
    [void](Update-LoginPromptState '' $StateFile {})
    exit 0
}
$code=Get-QuotaErrorCode $result.Output
if($code -ne 'not_logged_in'){
    exit $result.ExitCode
}
if(Test-Path -LiteralPath $StateFile){
    exit $result.ExitCode
}
[void](Update-LoginPromptState $code $StateFile {})
if(Start-LoginFlow){
    # A browser that still holds a console session completes the flow at once,
    # so a short poll can turn the failure into a silent recovery.
    $deadline=(Get-Date).AddSeconds($RecoveryTimeoutSeconds)
    while((Get-Date) -lt $deadline){
        Start-Sleep -Seconds 6
        $retry=Invoke-QuotaRefresh
        if($retry.ExitCode -eq 0){
            Remove-Item -LiteralPath $StateFile -Force -ErrorAction SilentlyContinue
            exit 0
        }
    }
}
Show-LoginPrompt
exit $result.ExitCode