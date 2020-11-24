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
        Expand-Archive -LiteralPath 'bizfly-backup.zip' -Force -DestinationPath 'C:\Windows\BackupAgent'
        Remove-Item 'bizfly-backup.zip'
    }
    
    function runAgentasService {
        $arch = GetArchitecture
        $download_url = "https://nssm.cc/release/nssm-2.24.zip"
        Invoke-WebRequest -Method Get -UseBasicParsing -Uri $download_url -OutFile "nssm.zip"
        Expand-Archive -LiteralPath 'nssm.zip' -Force -DestinationPath '.'
        if ($arch -eq "64bit"){
            Copy-Item -Path ".\nssm-2.24\win64\nssm.exe" "C:\Windows\BackupAgent"
        }else {
            Copy-Item -Path ".\nssm-2.24\win32\nssm.exe" "C:\Windows\BackupAgent"
        }
        Set-Location -Path 'C:\Windows\BackupAgent'
        Add-Content -Path "agent.yaml" -Value "access_key: $ACCESS_KEY`napi_url: $API_URL`nmachine_id: $MACHINE_ID`nsecret_key: $SECRET_KEY"
        .\nssm install BackupAgent "C:\Windows\BackupAgent\bizfly.exe"
        .\nssm set BackupAgent Application "C:\Windows\BackupAgent\bizfly.exe"
        .\nssm set BackupAgent AppParameters "agent --config=C:\Windows\BackupAgent\agent.yaml"
        .\nssm set BackupAgent AppThrottle 0
        .\nssm start BackupAgent
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

#Usage
#powershell -ExecutionPolicy Bypass -File test_pws.ps1 -ACCESS_KEY VGSG1NGLDALWHS9WKHQP -API_URL https://dev.bizflycloud.vn/api/cloud-backup -MACHINE_ID 8d8d2df2-3655-4315-9651-6751a81c94db -SECRET_KEY 05009fc456e1450d54225c8b4a599c581dee0d9d8ebe2aa6b6a55aee706387d5

# OR
#powershell -Command ("Invoke-WebRequest -Uri https://raw.githubusercontent.com/QuocCuong97/Code/master/PowerShell/install_agent.ps1 -OutFile agent.ps1") && ^
#powershell -ExecutionPolicy Bypass -File agent.ps1 ^
#-ACCESS_KEY VGSG1NGLDALWHS9WKHQP ^
#-API_URL https://dev.bizflycloud.vn/api/cloud-backup ^
#-MACHINE_ID 8d8d2df2-3655-4315-9651-6751a81c94db ^
#-SECRET_KEY 05009fc456e1450d54225c8b4a599c581dee0d9d8ebe2aa6b6a55aee706387d5