# Script install Agent for Backup Service on Windows

param (
    $ACCESS_KEY,
    $API_URL,
    $MACHINE_ID,
    $SECRET_KEY
)

function testAdministrator{
    $user = [Security.Principal.WindowsIdentity]::GetCurrent();
    (New-Object Security.Principal.WindowsPrincipal $user).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
}

function main {

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
            $filename = "bizfly-backup_windows_amd64.zip"
        }else {
            $filename = "bizfly-backup_windows_386.zip"
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
        Invoke-WebRequest -Method Get -UseBasicParsing -Uri $download_url -OutFile "bizfly-backup.zip"
        Expand-Archive -LiteralPath 'bizfly-backup.zip' -Force -DestinationPath 'C:\Windows\BizFlyBackup'
        Remove-Item 'bizfly-backup.zip'
    }

    function runAgentasService {
        $arch = GetArchitecture
        $download_url = "https://nssm.cc/release/nssm-2.24.zip"
        Invoke-WebRequest -Method Get -UseBasicParsing -Uri $download_url -OutFile "nssm.zip"
        Expand-Archive -LiteralPath 'nssm.zip' -Force -DestinationPath '.'
        if ($arch -eq "64bit"){
            Copy-Item -Path ".\nssm-2.24\win64\nssm.exe" "C:\Windows\BizFlyBackup"
        }else {
            Copy-Item -Path ".\nssm-2.24\win32\nssm.exe" "C:\Windows\BizFlyBackup"
        }
        Set-Location -Path 'C:\Windows\BizFlyBackup'
        Add-Content -Path "agent.yaml" -Value "access_key: $ACCESS_KEY`napi_url: $API_URL`nmachine_id: $MACHINE_ID`nsecret_key: $SECRET_KEY"
        .\nssm install BizFlyBackup "C:\Windows\BizFlyBackup\bizfly-backup.exe"
        .\nssm set BizFlyBackup Application "C:\Windows\BizFlyBackup\bizfly-backup.exe"
        .\nssm set BizFlyBackup AppParameters "agent --config=C:\Windows\BizFlyBackup\agent.yaml"
        .\nssm set BizFlyBackup AppThrottle 0
        .\nssm start BizFlyBackup
    }

    Set-Location -Path '~\'
    downloadAgent
    runAgentasService
}

if (testAdministrator){
    main
} else {
    Write-Host "Please run script as an administrator"
}
