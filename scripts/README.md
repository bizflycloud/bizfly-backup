# Scripts install Backup Service Agent
## **Install on Linux**
- [Script]()
### **Running**
- Switch to `root` user :
    ```sh
    sudo -sE
    ```
- Run script :
    ```sh
    ACCESS_KEY=OLIQSJ5EQKTRVB01HXJ0 \
    API_URL=https://dev.bizflycloud.vn/api/cloud-backup \
    MACHINE_ID=4a10ed55-812e-429b-a889-877ecae7088d \
    SECRET_KEY=791bc1fac71cef7acb77a4cb306352a5266ffe5f0749c8525d0cffd36c6c4207 \
    bash -c "$(curl -sSL https://raw.githubusercontent.com/QuocCuong97/Code/master/Bash/install_agent_linux.sh)"
    ```
## **Install on MacOS**
- [Script]()
### **Running**
- Switch to `root` user :
    ```sh
    sudo -sE
    ```
- Run script :
    ```sh
    ACCESS_KEY=1RTTIXPO9KAXH53ARDDB \
    API_URL=https://dev.bizflycloud.vn/api/cloud-backup \
    MACHINE_ID=d5655507-82d3-42b1-b276-52c7579ae713 \
    SECRET_KEY=3872a3eecb88812c7e2b20f1219dfec0682b86ddfe8b71d957bebdd5eae8d1a1 \
    bash -c "$(curl -sSL https://raw.githubusercontent.com/QuocCuong97/Code/master/Bash/install_agent_macos.sh)"
    ```
## **Install on Windows**
- [Script]()
### **Running**
- Open **Command Prompt (CMD)** or **PowerShell** with **administrator privileges** (*Run as administrator*)
- With CMD, run command :
    ```powershell
    powershell -Command ("Invoke-WebRequest -Uri https://raw.githubusercontent.com/QuocCuong97/Code/master/PowerShell/install_agent.ps1 -OutFile agent.ps1") && ^
    powershell -ExecutionPolicy Bypass -File agent.ps1 ^
    -ACCESS_KEY VGSG1NGLDALWHS9WKHQP ^
    -API_URL https://dev.bizflycloud.vn/api/cloud-backup ^
    -MACHINE_ID 8d8d2df2-3655-4315-9651-6751a81c94db ^
    -SECRET_KEY 05009fc456e1450d54225c8b4a599c581dee0d9d8ebe2aa6b6a55aee706387d5
    ```
- With Powershell run command :
    ```powershell
    powershell -Command ("Invoke-WebRequest -Uri https://raw.githubusercontent.com/QuocCuong97/Code/master/PowerShell/install_agent.ps1 -OutFile agent.ps1") && `
    powershell -ExecutionPolicy Bypass -File agent.ps1 `
    -ACCESS_KEY VGSG1NGLDALWHS9WKHQP `
    -API_URL https://dev.bizflycloud.vn/api/cloud-backup `
    -MACHINE_ID 8d8d2df2-3655-4315-9651-6751a81c94db `
    -SECRET_KEY 05009fc456e1450d54225c8b4a599c581dee0d9d8ebe2aa6b6a55aee706387d5
    ```