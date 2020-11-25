#!/bin/bash

# Script install Agent for Backup Service on Linux

check_distribution(){
    distribution_raw=$(cat /etc/os-release | grep ID_LIKE)
    # For Ubuntu/Debian
    if [[ $distribution_raw == *debian* ]] ; then
        sudo apt-get update -y
        sudo apt-get install -y jq curl
        echo "support"
    # For CentOS/RHEL 6,7,8
    elif [[ $distribution_raw == *rhel* || -f "/etc/redhat-release" ]] ; then
        yum install -y jq curl
        echo "support"
    else
        echo "not support"
    fi
}

get_lastest_download_url(){
    lastest_version=$(curl -X GET -s https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest | jq '.assets')
    length=$(echo $lastest_version | jq '. | length')
    arch=$(uname -m)
    if [[ $arch == x86_64 ]] ; then
        filename="bizfly-backup_linux_amd64.tar.gz"
    elif [[ $arch == i386 ]] ; then
        filename="bizfly-backup_linux_386.tar.gz"
    elif [[ $arch == arm ]] ; then
        filename="bizfly-backup_linux_arm64.tar.gz"
    else
        filename=""
    fi
    if [ -z "$filename" ]; then
        echo "not support"
    else
        i=0
        while [ $i -lt $length ]
        do
            download_url_raw=$(echo $lastest_version | jq -r .[$i].browser_download_url)
            if [[ $download_url_raw == *$filename* ]]; then
                download_url=$download_url_raw
                break
            fi
            i=$(( $i + 1 ))
        done
    fi
    echo $download_url
}

download_agent(){
    if [[ $(check_distribution) == "not support" ]]; then
        echo "Not support!"
    else
        if [[ $(get_lastest_download_url) == "not support" ]]; then
            echo "Not support!"
        else
            curl -Ls $(get_lastest_download_url) --output "bizfly-backup.tar.gz"
            tar -xzf bizfly-backup.tar.gz
            mv bizfly-backup /usr/bin
            rm -f bizfly-backup.tar.gz
        fi
    fi
}

run_agent_with_systemd(){
mkdir /etc/bizfly-backup/
    cat <<EOF > /etc/bizfly-backup/agent.yaml
access_key: $ACCESS_KEY
api_url: $API_URL
machine_id: $MACHINE_ID
secret_key: $SECRET_KEY
EOF
    cat <<EOF > /etc/systemd/system/bizfly-backup.service
[Unit]
Description=Backup Agent Service
[Service]
Type=simple
ExecStart=/usr/bin/bizfly-backup agent --config=/etc/bizfly-backup/agent.yaml
[Install]
WantedBy=multi-user.target
EOF
    sudo chmod 644 /etc/systemd/system/bizfly-backup.service
    systemctl enable bizfly-backup
    systemctl start bizfly-backup
    systemctl status bizfly-backup
}

clear
printf "=========================================================================\n"
printf "***********BizFly Backup Agent Installation - BizFly Cloud********************\n"
printf "=========================================================================\n"
printf "First Step: Download BizFly Backup Agent\n"
printf "====================================\n"
download_agent

clear
printf "=========================================================================\n"
printf "Second Step: Run BizFly Backup Agent\n"
printf "=======================================\n"
run_agent_with_systemd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY


# START SERVICE:
# systemctl start bizfly-backup

# STOP SERVICE:
# systemctl stop bizfly-backup
