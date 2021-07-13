#!/bin/bash

conf_file=""

declare -a netnses

declare -A pfs
declare -A pcis
declare -A pf_port_names
declare -A pf_switch_ids

TIMEOUT="${TIMEOUT:-2}"
POLL_INTERVAL="1"

while test $# -gt 0; do
  case "$1" in

   --netns | -n)
      netnses+=("$2")
      shift
      shift
      ;;

   --pf | -d)
      if [[ -z "${netnses[-1]}" ]];then
          echo "ERROR: No netns is specified, please provide one using the --netns before using --pf."
          exit 1
      fi

      pfs["${netnses[-1]}"]+="$2 "
      shift
      shift
      ;;

   --conf-file | -c)
      conf_file=$2
      shift
      shift
      ;;

   --help | -h)
      echo "
vf-netns-switcher.sh --netns <> --pf <> [--conf-file <>]:

	--netns | -n		Netns to switch the interface PFs and VFs to. This needs to be provided before the pfs
				to associate the pfs to the netns. This flag can be repeated.

	--pf | -d		The PF to switch it and its VFs to the specified netns. This must be provided after a
				netns to associate the pf to the last provided netns. This option can be repeated.

	--conf-file | -c	A file to read confs from, this will override cli flags. Conf file should be of the form:
				- netns: <netns1>
				  pfs:
				  - <pf1>
				  - <pf2>
				- netns: <netns2>
				  pfs:
				  - <pf3>
				  - <pf4>

"
      exit 0
      ;;

   *)
      echo "No such option!!"
      echo "Exitting ...."
      exit 1
  esac
done

get_pcis_from_pfs(){
    local worker_netns="${1}"
    shift
    local interfaces="$@"
    for interface in $interfaces; do
        pcis["$interface"]="$(get_pci_from_net_name "$interface" "$worker_netns")"
    done
}

get_pci_from_net_name(){
    local interface_name=$1
    local worker_netns="${2:-$netns}"

    if [[ -z "$(ip l show $interface_name)" ]];then
        if [[ -n "$(docker exec -t ${worker_netns} ip l show $interface_name)" ]];then
            ip netns exec ${worker_netns} bash -c "basename \$(readlink /sys/class/net/${interface_name}/device)"
            return 0
        fi
        echo ""
        return 1
    fi
    basename $(readlink /sys/class/net/${interface_name}/device)
}

netns_create(){
    local worker_netns="${1:-$netns}"

    if [[ ! -e /var/run/netns/$worker_netns ]];then
        local pid="$(docker inspect -f '{{.State.Pid}}' $worker_netns)"

        if [[ -z "$pid" ]];then
            return 1
        fi

        mkdir -p /var/run/netns/
        rm -rf /var/run/netns/$worker_netns
        ln -sf /proc/$pid/ns/net "/var/run/netns/$worker_netns"

        if [[ -z "$(ip netns | grep $worker_netns)" ]];then
            return 1
        fi
    fi
    return 0
}

switch_pfs(){
    local worker_netns="${1:-$netns}"
    shift
    local interfaces="$@"

    for pf in $interfaces;do
        switch_pf "$pf" "$worker_netns"
    done
}

switch_pf(){
    local pf_name="$1"
    local worker_netns="${2:-$netns}"

    if [[ -z "$(ip netns | grep ${worker_netns})" ]];then
        echo "Namespace $worker_netns not found!"
        return 1
    fi

    if [[ -z "$(ip l show ${pf_name})" ]];then
        if [[ -z "$(docker exec -t ${worker_netns} ip l show ${pf_name})" ]];then
            echo "Interface $pf_name not found..."
            return 1
        fi

        echo "PF ${pf_name} already in namespace $worker_netns!"
    else
        if ! ip l set dev $pf_name netns $worker_netns;then
            echo "Error: unable to set $pf_name namespace to $worker_netns!"
            return 1
        fi
    fi

    if ! docker exec -t ${worker_netns} ip l set $pf_name up;then
        echo "Error: unable to set $pf_name to up!"
        return 1
    fi
    
}

switch_vf(){
    local vf_name="$1"
    local worker_netns="${2:-$netns}"

    if [[ -z "$(ip l show $vf_name)" ]];then
        return 1
    fi

    if ip link set "$vf_name" netns "$worker_netns"; then
      if timeout "$TIMEOUT"s bash -c "until ip netns exec $worker_netns ip link show $vf_name > /dev/null; do sleep $POLL_INTERVAL; done"; then
          return 0
      else
          return 1
      fi
    fi
}

switch_netns_vfs(){
    local worker_netns="${1:-$netns}"

    for pf in ${pfs["$worker_netns"]};do
        switch_interface_vfs "$pf" "$worker_netns" "${pcis[$pf]}"
    done
}

get_pf_switch_dev_info(){
   local worker_netns="${1}"
    shift
    local interfaces="$@"
    for interface in $interfaces; do
        interface_pci_address="${pcis[$interface]}"
        if grep -q 'siwtchdev' <(devlink dev eswitch show pci/$interface_pci_address ); then
          continue
        fi
        pf_port_names["$interface"]="$(cat /sys/class/net/${interface}/phys_port_name)"
        pf_switch_ids["$interface"]="$(cat /sys/class/net/${interface}/phys_switch_id)"
    done
}

switch_netns_vf_representors(){
    local worker_netns="${1:-$netns}"
    for pf in ${pfs["$worker_netns"]};do
        switch_interface_vf_representors "$pf" "$worker_netns"
    done
}

switch_interface_vf_representors(){
    local pf_name="$1"
    local worker_netns=$2

    if [[ -z "${pf_switch_ids[$pf_name]}" ]] || [[ -z ${pf_port_names[$pf_name]:1} ]];then
        echo "$pf_name does not have pf_switch_id or pf_port_name, assuming not switchdev..."
        return 0
    fi

    for interface in $(ls /sys/class/net);do
        phys_switch_id=$(cat /sys/class/net/$interface/phys_switch_id)
        if [[ "$phys_switch_id" != "${pf_switch_ids[$pf_name]}" ]]; then
            continue
        fi
        phys_port_name=$(cat /sys/class/net/$interface/phys_port_name)
        phys_port_name_pf_index=${phys_port_name%vf*}
        phys_port_name_pf_index=${phys_port_name_pf_index#pf}
        if [[ "$phys_port_name_pf_index" != "${pf_port_names[$pf_name]:1}"  ]]; then
            continue
        fi
        echo "Switching VF representor $interface of PF $pf_name to netns $worker_netns"
        switch_vf $interface $worker_netns
    done
}

switch_interface_vfs(){
    local pf_name="$1"
    local worker_netns="${2:-$netns}"
    local pci="$3"

    vfs_list=$(ls /sys/bus/pci/devices/$pci | grep virtfn)

    if [[ -z "${vfs_list}" ]];then
        echo "Warning: No VFs found for interface $pf_name!!"
        return 0
    fi

    for vf in $vfs_list;do
        local vf_interface="$(ls /sys/bus/pci/devices/$pci/$vf/net)"

        if [[ -n "$vf_interface" ]];then
            echo "Switching $vf_interface to namespace $worker_netns..."
            sleep 2
            if ! switch_vf "$vf_interface" "$worker_netns";then
                echo "Error: could not switch $vf_interface to namespace $worker_netns!"
            else
                echo "Successfully switched $vf_interface to namespace $worker_netns"
            fi
        fi
    done
}

read_confs(){
    local conf_file="$1"

    let number_of_netns=$(yq r $conf_file -l)-1

    for index in $(seq 0 $number_of_netns);do
        netnses+=("$(yq r $conf_file [$index].netns)")
        let number_of_pfs=$(yq r $conf_file [$index].pfs -l)-1
        for pf_index in $(seq 0 $number_of_pfs);do
            pfs[${netnses[-1]}]+="$(yq r $conf_file [$index].pfs[$pf_index]) "
        done
    done
}

variables_check(){
    local status=0

    check_empty_var "netns"
    let status=$status+$?
    check_empty_var "pfs"
    let status=$status+$?

    return $status
}

check_empty_var(){
    local var_name="$1"

    if [[ -z "${!var_name[@]}" ]];then
        echo "$var_name is empty..."
        return 1
    fi

    return 0
}

main(){
    local status=0

    while true;do
        for netns in ${netnses[@]};do
            switch_pfs "$netns" "${pfs[$netns]}"
            sleep 2
            switch_netns_vfs "$netns"
            sleep 2
            switch_netns_vf_representors "$netns" "${pfs[$netns]}"
        done
        sleep $TIMEOUT
    done
    return $status
}

if [[ -n "$conf_file" ]];then
    unset netnses
    unset pfs

    declare -a netnses
    declare -A pfs

    read_confs "$conf_file"
fi

variables_check
let status=$status+$?
if [[ "$status" != "0" ]];then
    echo "ERROR: empty var..."
    exit $status
fi

for netns in ${netnses[@]};do
    netns_create "$netns"
    let status=$status+$?
    if [[ "$status" != "0" ]];then
        echo "ERROR: failed to create netns..."
        exit $status
    fi
done

for netns in ${netnses[@]};do
    get_pcis_from_pfs "$netns" "${pfs[$netns]}"
    get_pf_switch_dev_info "$netns" "${pfs[$netns]}"
done

if [[ "${#pcis[@]}" == "0" ]];then
    echo "Error: could not get pci address of interface $pf!!"
    exit 1
fi

main
