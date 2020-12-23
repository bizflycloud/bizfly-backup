#!/bin/bash

# Script install Agent for Backup Service on Linux

check_distribution(){
    . /etc/os-release
    case $ID in
    # For Ubuntu/Debian/Kali
        ubuntu | debian | kali)
            sudo apt-get update -y
            sudo apt-get install -y jq
            echo "support"
            ;;
    # For CentOS/RHEL 7,8
        rhel | centos)
            yum install -y jq
            echo "support"
            ;;
    # For OpenSuse
        *suse*)
            zypper install -y jq
            echo "support"
            ;;
        *)
            echo "not support"
            ;;
    esac
}

get_lastest_download_url(){
    lastest_version=$(curl -X GET -s https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest | jq '.assets')
    length=$(echo $lastest_version | jq '. | length')
    arch=$(uname -m)
    if [[ $arch == x86_64 ]] ; then
        filename="bizfly-backup_linux_amd64"
    elif [[ $arch == i386 ]] ; then
        filename="bizfly-backup_linux_386"
    elif [[ $arch == arm ]] ; then
        filename="bizfly-backup_linux_arm64"
    else
        filename=""
    fi
    if [ -z "$filename" ]; then
        echo "not support"
    else
        i=0
        while [ "$i" -lt "$length" ]
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
        if [[ "$(get_lastest_download_url)" == "not support" ]]; then
            echo "Not support!"
        else
            curl -Ls "$(get_lastest_download_url)" --output "bizfly-backup"
            chmod +x bizfly-backup
            mv bizfly-backup /usr/bin
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
    systemctl restart bizfly-backup
    systemctl status bizfly-backup
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
    run_agent_with_systemd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY
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
    systemctl stop bizfly-backup
    rm -f /tmp/bizfly-backup.sock /usr/bin/bizfly-backup
    download_agent

    clear
    printf "=========================================================================\n"
    printf "Second Step: Run BizFly Backup Agent\n"
    printf "====================================\n"
    run_agent_with_systemd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY
    printf "====================================\n"
    printf "Your agent is successfully updated !\n"
    printf "====================================\n"
}

main(){
    if [[ -x $(command -v bizfly-backup) ]] ; then
        installed_version=$(bizfly-backup version | grep Version | awk '{print $2}' | sed 's/v//g')
        lastest_version=$(curl -X GET -s https://api.github.com/repos/bizflycloud/bizfly-backup/releases/latest | jq '.tag_name' | sed 's/["v]//g')
        if [[ "$installed_version" == $lastest_version ]] ; then
            clear
            printf "=========================================================================\n"
            printf "Run BizFly Backup Agent\n"
            printf "=======================\n"
            run_agent_with_systemd ACCESS_KEY API_URL MACHINE_ID SECRET_KEY
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
# systemctl start bizfly-backup

# STOP SERVICE:
# systemctl stop bizfly-backup