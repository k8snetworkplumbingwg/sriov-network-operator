#!/bin/bash
set -x

command=$1
shift
declare -a kargs=( "$@" )
ret=0
args=$(chroot /host/ cat /proc/cmdline)

IS_OS_UBUNTU=true; [[ "$(chroot /host/ grep -i ubuntu /etc/os-release -c)" == "0" ]] && IS_OS_UBUNTU=false

if ${IS_OS_UBUNTU} ; then
    grub_config="/etc/default/grub"
    # Operate on the copy of the file
    cp /host/${grub_config} /tmp/grub

    for t in "${kargs[@]}";do
        if [[ $command == "add" ]];then
            # Modify only GRUB_CMDLINE_LINUX_DEFAULT line if it's not already present
            line=$(grep -P "^\s*GRUB_CMDLINE_LINUX_DEFAULT" /tmp/grub)
            if [ $? -ne 0 ];then
                exit 1
            fi

            IFS='"' read g param q <<< "$line"
            arr=($param)
            found=false

            for item in "${arr[@]}"; do
                if [[ "$item" == "${t}" ]]; then
                    found=true
                    break
                fi
            done

            if [ $found == false ];then
                # Append to the end of the line
                t="${arr[@]} ${t}"
                sed -i "s/\(^\s*GRUB_CMDLINE_LINUX_DEFAULT=\"\)\(.*\)\"/\1${t}\"/" /tmp/grub
                let ret++
            fi
        fi

        if [[ $command == "remove" ]];then
            # Remove from everywhere, except commented lines
            ret=$((ret + $(grep -E '^[[:space:]]*GRUB_CMDLINE_LINUX(_DEFAULT)?[[:space:]]*=.*(^|[[:space:]]|")'"$t"'([[:space:]]|"|$)' /tmp/grub | wc -l)))
            if [ $ret -gt 0 ];then
                while read line;do
                    if [[ "$line" =~ GRUB_CMDLINE_LINUX ]];then
                        IFS='"' read g param q <<< "$line"

                        arr=($param)
                        new_param=""

                        for item in "${arr[@]}"; do
                            if [[ "$item" != "${t}" ]]; then
                                new_param="${new_param} ${item}"
                            fi
                        done
                        sed -i "s/\(^\s*$g\"\)\(.*\)\"/\1${new_param}\"/" /tmp/grub
                    fi
                done < /tmp/grub
            fi
        fi
    done

    if [ $ret -ne 0 ];then
        # Update grub only if there were changes
        cp /tmp/grub /host/${grub_config}
        chroot "/host" update-grub
    fi

    echo $ret
    exit 0
fi

if chroot /host/ test -f /run/ostree-booted ; then
    for t in "${kargs[@]}";do
        if [[ $command == "add" ]];then
          if [[ $args != *${t}* ]];then
              if chroot /host/ rpm-ostree kargs | grep -vq ${t}; then
                  chroot /host/ rpm-ostree kargs --append ${t} > /dev/null 2>&1
              fi
              let ret++
          fi
        fi
        if [[ $command == "remove" ]];then
          if [[ $args == *${t}* ]];then
                if chroot /host/ rpm-ostree kargs | grep -q ${t}; then
                    chroot /host/ rpm-ostree kargs --delete ${t} > /dev/null 2>&1
                fi
                let ret++
            fi
        fi
    done
else
    chroot /host/ which grubby > /dev/null 2>&1
    # if grubby is not there, let's tell it
    if [ $? -ne 0 ]; then
        exit 127
    fi
    for t in "${kargs[@]}";do
      if [[ $command == "add" ]];then
        if [[ $args != *${t}* ]];then
            if chroot /host/ grubby --info=DEFAULT | grep args | grep -vq ${t}; then
                chroot /host/ grubby --update-kernel=DEFAULT --args=${t} > /dev/null 2>&1
            fi
            let ret++
        fi
      fi
      if [[ $command == "remove" ]];then
          if [[ $args == *${t}* ]];then
            if chroot /host/ grubby --info=DEFAULT | grep args | grep -q ${t}; then
                chroot /host/ grubby --update-kernel=DEFAULT --remove-args=${t} > /dev/null 2>&1
            fi
            let ret++
          fi
      fi
    done
fi

echo $ret
