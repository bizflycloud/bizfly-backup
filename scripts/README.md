# Scripts install Backup Service Agent
## **Install on Linux**
- [Script](https://github.com/bizflycloud/bizfly-backup/blob/master/scripts/linux.sh)
### **Running**
- Run script :
    ```sh
    ACCESS_KEY=<your_access_key> \
    API_URL=https://api-backup-hn.manage.bizflycloud.vn \
    MACHINE_ID=<your_machine_id> \
    SECRET_KEY=<your_secret_key> \
    sudo -E bash -c "$(curl -sSL https://get.bizflycloud.vn/cloud-backup/linux.sh)"
    ```
## **Install on MacOS**
- [Script](https://github.com/bizflycloud/bizfly-backup/blob/master/scripts/macos.sh)
### **Running**
- Run script :
    ```sh
    ACCESS_KEY=<your_access_key> \
    API_URL=https://api-backup-hn.manage.bizflycloud.vn \
    MACHINE_ID=<your_machine_id> \
    SECRET_KEY=<your_secret_key> \
    sudo -E bash -c "$(curl -sSL https://get.bizflycloud.vn/cloud-backup/macos.sh)"
    ```
## **Install on Windows**
- [Script](https://github.com/bizflycloud/bizfly-backup/blob/master/scripts/windows.ps1)
### **Running**
- Open **Command Prompt (CMD)** or **PowerShell** with **administrator privileges** (*Run as administrator*)
- With CMD, run command :
    ```powershell
    powershell -Command ("Invoke-WebRequest -Uri https://get.bizflycloud.vn/cloud-backup/windows.ps1 -OutFile agent.ps1") && ^
    powershell -ExecutionPolicy Bypass -File agent.ps1 ^
    -ACCESS_KEY <your_access_key> ^
    -API_URL https://api-backup-hn.manage.bizflycloud.vn ^
    -MACHINE_ID <your_machine_id> ^
    -SECRET_KEY <your_secret_key>
    ```
- With Powershell run command :
    ```powershell
    powershell -Command ("Invoke-WebRequest -Uri https://get.bizflycloud.vn/cloud-backup/windows.ps1 -OutFile agent.ps1") ; `
    powershell -ExecutionPolicy Bypass -File agent.ps1 `
    -ACCESS_KEY <your_access_key> `
    -API_URL https://api-backup-hn.manage.bizflycloud.vn `
    -MACHINE_ID <your_machine_id> `
    -SECRET_KEY <your_secret_key>
    ```