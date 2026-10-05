#!/bin/bash

udev_base_path="${UDEV_BASE_PATH:-/etc/udev}"
target_dir="/host${udev_base_path}"
target_file="${target_dir}/disable-nm-sriov.sh"

mkdir -p "${target_dir}"

cat <<'EOF' > "${target_file}"
#!/bin/bash
if [ ! -d "/sys/class/net/$1/device/physfn" ]; then
    exit 0
fi

pf_path=$(readlink /sys/class/net/$1/device/physfn -n)
pf_pci_address=${pf_path##*../}

if [ "$2" == "$pf_pci_address" ]; then
    echo "NM_UNMANAGED=1"
fi
EOF

chmod +x "${target_file}"
