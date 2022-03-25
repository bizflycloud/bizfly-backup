# Script install Agent for Backup Service on Windows

param (
    $ACCESS_KEY,
    $API_URL,
    $MACHINE_ID,
    $SECRET_KEY
)

function checkAdministrator{
    $user = [Security.Principal.WindowsIdentity]::GetCurrent();
    (New-Object Security.Principal.WindowsPrincipal $user).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
}

function getArchitecture {
    if ($([Environment]::Is64BitOperatingSystem)){
        Write-Output "64bit"
    }else {
        Write-Output "32bit"
    }
}

function getDownloadURL {

    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls, [Net.SecurityProtocolType]::Tls11, [Net.SecurityProtocolType]::Tls12, [Net.SecurityProtocolType]::Ssl3
    [Net.ServicePointManager]::SecurityProtocol = "Tls, Tls11, Tls12, Ssl3"

    $release_url = "https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest"
    $response = (Invoke-WebRequest -UseBasicParsing -Uri $release_url)
    $responseobj = (ConvertFrom-Json -InputObject $response).assets
    $arch = GetArchitecture
    if ($arch -eq "64bit"){
        $filename = "bizfly-backup_windows_amd64.exe"
    }else {
        $filename = "bizfly-backup_windows_386.exe"
    }
    for($i = 0; $i -lt $responseobj.length; $i++){
        if ($responseobj[$i].browser_download_url -like "*$filename*"){
            Write-Output $responseobj[$i].browser_download_url
            break
        }
    }
}

function downloadAgent {
    $download_url = getDownloadURL
    Invoke-WebRequest -Method Get -UseBasicParsing -Uri $download_url -OutFile ( New-Item -Path "C:\progra~1\BizFlyBackup\bizfly-backup.exe" )
}

function updateConfig {
    if (($ACCESS_KEY -ne $null) -And ($API_URL -ne $null) -And ($MACHINE_ID -ne $null) -And ($SECRET_KEY -ne $null)) {
        if ([System.IO.File]::Exists("C:\progra~1\BizFlyBackup\agent.yaml"))
        {
            Remove-Item "agent.yaml"
        }
        Add-Content -Path "agent.yaml" -Value "access_key: $ACCESS_KEY`napi_url: $API_URL`nmachine_id: $MACHINE_ID`nsecret_key: $SECRET_KEY"
    }
}

function runAgentasService {
    if ([System.IO.File]::Exists("C:\progra~1\BizFlyBackup\bizfly-backup.exe") -And [System.IO.File]::Exists("C:\progra~1\BizFlyBackup\nssm.exe")){
        Set-Location -Path "C:\progra~1\BizFlyBackup"
        updateConfig
        .\nssm restart BizFlyBackup
    }else {
        $arch = GetArchitecture
        $download_url = "https://nssm.cc/release/nssm-2.24.zip"
        Invoke-WebRequest -Method Get -UseBasicParsing -Uri $download_url -OutFile "nssm.zip"
        Expand-Archive -LiteralPath 'nssm.zip' -Force -DestinationPath '.'
        if ($arch -eq "64bit"){
            Copy-Item -Path ".\nssm-2.24\win64\nssm.exe" "C:\progra~1\BizFlyBackup"
        }else {
            Copy-Item -Path ".\nssm-2.24\win32\nssm.exe" "C:\progra~1\BizFlyBackup"
        }
        Set-Location -Path "C:\progra~1\BizFlyBackup"
        updateConfig
        .\nssm install BizFlyBackup "C:\progra~1\BizFlyBackup\bizfly-backup.exe"
        .\nssm set BizFlyBackup Application "C:\progra~1\BizFlyBackup\bizfly-backup.exe"
        .\nssm set BizFlyBackup AppParameters "agent --config=C:\progra~1\BizFlyBackup\agent.yaml"
        .\nssm set BizFlyBackup AppThrottle 0
        .\nssm set BizFlyBackup AppExit 0 Restart
        .\nssm start BizFlyBackup
        Remove-Item "~\nssm.zip"
        Remove-Item "~\nssm-2.24" -Recurse
    }
}

function fullInstall {
    Clear-Host
    Set-Location -Path "~\"
    New-Item -ItemType Directory -Path "C:\progra~1\BizFlyBackup" | Out-Null
    Write-Host "=========================================================================`n"
    Write-Host "********** BizFly Backup Agent Installation - BizFly Cloud **************`n"
    Write-Host "=========================================================================`n"
    Write-Host "First Step: Download BizFly Backup Agent`n"
    Write-Host "========================================`n"
    downloadAgent

    Clear-Host
    Write-Host "=========================================================================`n"
    Write-Host "Second Step: Run BizFly Backup Agent`n"
    Write-Host "====================================`n"
    runAgentasService
}

function upgrade {
    Clear-Host
    Write-Host "=========================================================================`n"
    Write-Host "********** BizFly Backup Agent Installation - BizFly Cloud **************`n"
    Write-Host "=========================================================================`n"
    Write-Host "First Step: Upgrading BizFly Backup Agent`n"
    Write-Host "=========================================`n"

    Stop-Service -Name "BizFlyBackup"
    Remove-Item "C:\progra~1\BizFlyBackup\bizfly-backup.exe"
    Remove-Item "~\AppData\Local\Temp\bizfly-backup.sock" -Force -ErrorAction SilentlyContinue
    Set-Location -Path "~\"
    downloadAgent

    Clear-Host
    Write-Host "=========================================================================`n"
    Write-Host "Second Step: Run BizFly Backup Agent`n"
    Write-Host "====================================`n"
    runAgentasService
}

if (checkAdministrator){
    if ([System.IO.File]::Exists("C:\progra~1\BizFlyBackup\bizfly-backup.exe")){
        $current_version = $((\progra~1\BizFlyBackup\bizfly-backup.exe version | Select-String "Version:")  -split ":  ")[1]

        $release_url = "https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest"
        $response = (Invoke-WebRequest -UseBasicParsing -Uri $release_url)
        $lastest_version = (ConvertFrom-Json -InputObject $response).tag_name
        if ("v$current_version" -eq $lastest_version){
            Clear-Host
            Write-Host "=========================================================================`n"
            Write-Host "Run BizFly Backup Agent`n"
            Write-Host "=======================`n"
            runAgentasService
        }else {
            Write-Host "`n=========================================================================`n"
            Write-Host "A new version of bizfly-backup ($lastest_version) is available!`n"
            $ans = Read-Host "Do you want to start the upgrade? [Y/n]"
            $yes_ans = @("y", "Y", "Yes")
            $no_ans = @("n", "N", "No")
            if ($yes_ans -Contains $ans){
                upgrade
            }elseif ($no_ans -Contains $ans){
                exit
            }else {
                Write-Host "Invalid input..."
                exit
            }
        }
    }else {
        fullInstall
    }
} else {
    Write-Host "Please run script as administrator"
}