#!/bin/bash

# Script install Agent for Backup Service on MAC OS

get_latest_release() {
    lastest_version=$(curl -s "https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    download_url="https://github.com/bizflycloud/bizfly-backup/releases/download/$lastest_version/bizfly-backup_darwin_amd64"
    echo $download_url
}

download_agent() {
    download_url=$(get_latest_release)
    curl -fsSL $download_url -o "bizfly-backup"
    chmod +x bizfly-backup
    if [[ -f "/usr/local/bin" ]]; then
        rm -f /usr/local/bin
    fi
    if [[ ! -d "/usr/local/bin" ]]; then
        mkdir /usr/local/bin
    fi
    if [[ ! -f "/etc/paths.d/bizfly-backup" ]]; then
        echo /usr/local/bin/ > /etc/paths.d/bizfly-backup
    fi
    mv bizfly-backup /usr/local/bin/
}

run_agent_with_launchd(){
    sudo cat <<EOF > /Library/LaunchDaemons/bizfly.backup.plist
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>bizfly-backup</string>
    <key>ProgramArguments</key>
    <array>
      <string>/usr/local/bin/bizfly-backup</string>
      <string>--config</string>
      <string>/etc/bizfly-backup/agent.yaml</string>
      <string>agent</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
    <key>LaunchOnlyOnce</key>
    <false/>
    <key>StandardOutPath</key>
    <string>/tmp/bizfly-backup.stdout</string>
    <key>StandardErrorPath</key>
    <string>/tmp/bizfly-backup.stderr</string>
  </dict>
</plist>
EOF

    mkdir /etc/bizfly-backup
    cat <<EOF > /etc/bizfly-backup/agent.yaml
access_key: $ACCESS_KEY
api_url: $API_URL
machine_id: $MACHINE_ID
secret_key: $SECRET_KEY
EOF

    launchctl load -w /Library/LaunchDaemons/bizfly.backup.plist
    launchctl start bizfly-backup
    launchctl list bizfly-backup
}

full_install(){
    clear
    printf "=========================================================================\n"
    printf "********** BizFly Backup Agent Installation - BizFly Cloud **************\n"
    printf "=========================================================================\n"
    printf "First Step: Download BizFly Backup Agent\n"
    printf "========================================\n"
    download_agent

    clear
    printf "=========================================================================\n"
    printf "Second Step: Run BizFly Backup Agent\n"
    printf "====================================\n"
    run_agent_with_launchd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY
    printf "======================================\n"
    printf "Your agent is successfully installed !\n"
    printf "======================================\n"
}

upgrade(){
    clear
    printf "=========================================================================\n"
    printf "********** BizFly Backup Agent Installation - BizFly Cloud **************\n"
    printf "=========================================================================\n"
    printf "First Step: Upgrading BizFly Backup Agent\n"
    printf "=========================================\n"
    launchctl stop bizfly-backup
    launchctl unload -w /Library/LaunchDaemons/bizfly.backup.plist
    rm -Rf /etc/bizfly-backup /usr/local/bin/bizfly-backup /Library/LaunchDaemons/bizfly.backup.plist
    rm -f /tmp/bizfly-backup.sock /tmp/bizfly-backup.stderr /tmp/bizfly-backup.stdout
    download_agent

    clear
    printf "=========================================================================\n"
    printf "Second Step: Run BizFly Backup Agent\n"
    printf "====================================\n"
    run_agent_with_launchd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY
    printf "====================================\n"
    printf "Your agent is successfully updated !\n"
    printf "====================================\n"
}

main(){
    if [[ -x $(command -v bizfly-backup) ]] ; then
        installed_version=$(bizfly-backup version | grep Version | awk '{print $2}' | sed 's/v//g')
        lastest_version=$(curl -s "https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
        if [[ "$installed_version" == $lastest_version ]] ; then
            clear
            printf "=========================================================================\n"
            printf "Run BizFly Backup Agent\n"
            printf "=======================\n"
            launchctl stop bizfly-backup
            run_agent_with_launchd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY
            printf "=====================================\n"
            printf "Your agent is successfully installed!\n"
            printf "=====================================\n"
        else
            clear
            printf "=========================================================================\n"
            printf "A new version of bizfly-backup ($lastest_version) is available!\n"
            read -r -p "Do you want to start the upgrade? [Y/n]" input
            case $input in
                [yY][eE][sS]|[yY])
                    upgrade
                    ;;
                [nN][oO]|[nN])
                    exit
                    ;;
                *)
                    echo "Invalid input..."
                    exit
                    ;;
            esac
        fi
    else
        full_install
    fi
}

main


# START SERVICE:
# sudo launchctl start bizfly-backup

# STOP SERVICE:
# sudo launchctl stop bizfly-backup