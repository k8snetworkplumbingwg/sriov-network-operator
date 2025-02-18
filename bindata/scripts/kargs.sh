#!/bin/bash
set -x

command=$1
shift
declare -a kargs=( "$@" )
ret=0

if [[ -d "/bindata/scripts" ]];then
    chroot_path="/host/"
else
    chroot_path="/"
fi

args=$(chroot "$chroot_path" cat /proc/cmdline)

IS_OS_UBUNTU=true; [[ "$(chroot "$chroot_path" grep -i ubuntu /etc/os-release -c)" == "0" ]] && IS_OS_UBUNTU=false

if ${IS_OS_UBUNTU} ; then
    grub_config="/etc/default/grub"
    grub_cmdline=$(grep GRUB_CMDLINE_LINUX_DEFAULT grub)
    grub_cmdline=${grub_cmdline//"GRUB_CMDLINE_LINUX_DEFAULT="/}
    grub_cmdline="${grub_cmdline//\"}"

    for t in "${kargs[@]}";do
        if [[ $command == "add" ]];then
          if [[ $args != *${t}* ]];then
              grub_cmdline="$grub_cmdline $t"
              let ret++
          fi
        fi
        if [[ $command == "remove" ]];then
          if [[ $grub_cmdline == *${t}* ]];then
                grub_cmdline=$(echo $grub_cmdline  | sed "s/\b$t\b//g" | sed 's/  */ /g')
                let ret++
           fi
        fi
    done

    chroot "$chroot_path" sed -i "/GRUB_CMDLINE_LINUX_DEFAULT=.*/c\GRUB_CMDLINE_LINUX_DEFAULT=\"$grub_cmdline\"" $grub_config
    chroot "$chroot_path" update-grub

    echo $ret
    exit 0
fi

if chroot "$chroot_path" test -f /run/ostree-booted ; then
    for t in "${kargs[@]}";do
        if [[ $command == "add" ]];then
          if [[ $args != *${t}* ]];then
              if chroot "$chroot_path" rpm-ostree kargs | grep -vq ${t}; then
                  chroot "$chroot_path" rpm-ostree kargs --append ${t} > /dev/null 2>&1
              fi
              let ret++
          fi
        fi
        if [[ $command == "remove" ]];then
          if [[ $args == *${t}* ]];then
                if chroot "$chroot_path" rpm-ostree kargs | grep -q ${t}; then
                    chroot "$chroot_path" rpm-ostree kargs --delete ${t} > /dev/null 2>&1
                fi
                let ret++
            fi
        fi
    done
else
    chroot "$chroot_path" which grubby > /dev/null 2>&1
    # if grubby is not there, let's tell it
    if [ $? -ne 0 ]; then
        exit 127
    fi
    for t in "${kargs[@]}";do
      if [[ $command == "add" ]];then
        if [[ $args != *${t}* ]];then
            if chroot "$chroot_path" grubby --info=DEFAULT | grep args | grep -vq ${t}; then
                chroot "$chroot_path" grubby --update-kernel=DEFAULT --args=${t} > /dev/null 2>&1
            fi
            let ret++
        fi
      fi
      if [[ $command == "remove" ]];then
          if [[ $args == *${t}* ]];then
            if chroot "$chroot_path" grubby --info=DEFAULT | grep args | grep -q ${t}; then
                chroot "$chroot_path" grubby --update-kernel=DEFAULT --remove-args=${t} > /dev/null 2>&1
            fi
            let ret++
          fi
      fi
    done
fi

echo $ret
