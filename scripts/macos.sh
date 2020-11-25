#!/bin/bash

# Script install Agent for Backup Service on MAC OS

get_latest_release() {
    lastest_version=`curl -s "https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/'`
    download_url="https://github.com/bizflycloud/bizfly-backup/releases/download/$lastest_version/bizfly-backup_darwin_amd64.tar.gz"
    echo $download_url
}

download_agent() {
    download_url=$(get_latest_release)
    curl -fsSL $download_url -o "bizfly-backup.tar.gz"
    mkdir ~/.bizfly-backup
    tar -xzf bizfly-backup.tar.gz -C ~/.bizfly-backup/
}

run_agent_with_launchd(){
    cat <<EOF > /Library/LaunchDaemons/bizfly.backup.plist
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>bizfly-backup</string>
    <key>ProgramArguments</key>
    <array>
      <string>$HOME/.bizfly-backup/bizfly-backup</string>
      <string>--config</string>
      <string>$HOME/.bizfly-backup/agent.yaml</string>
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

    cat <<EOF > ~/.bizfly-backup/agent.yaml
access_key: $ACCESS_KEY
api_url: $API_URL
machine_id: $MACHINE_ID
secret_key: $SECRET_KEY
EOF

    launchctl load -w /Library/LaunchDaemons/bizfly.backup.plist
    launchctl list bizfly-backup
}

clear
printf "=========================================================================\n"
printf "******************Backup Agent Installation - BizFly Cloud********************\n"
printf "=========================================================================\n"
printf "First Step: Download Agent\n"
printf "====================================\n"
download_agent

clear
printf "=========================================================================\n"
printf "Second Step: Run Agent\n"
printf "=======================================\n"
run_agent_with_launchd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY


# START SERVICE:
# sudo launchctl start bizfly-backup

# STOP SERVICE:
# sudo launchctl stop bizfly-backup
