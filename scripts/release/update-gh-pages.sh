#!/bin/bash -e
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o pipefail

this=`basename $0`

usage () {
cat << EOF
Usage: $this [-h] [-r remote name] <helm-chart>

Options:
  -h         show this help and exit
  -r REMOTE  SR-IOV Network Operator remote repo
EOF
}

#
# Argument parsing
#
while getopts "hap:" opt; do
    case $opt in
        h)  usage
            exit 0
            ;;
        r)  remote="$OPTARG"
            ;;
        *)  usage
            exit 1
            ;;
    esac
done

shift "$((OPTIND - 1))"

# Check that no extra args were provided
if [ $# -ne 1 ]; then
    echo "ERROR: extra positional arguments: $@"
    usage
    exit 1
fi

chart="$1"
release=${chart::-4}

remote_url=${remote:-"https://github.com/k8snetworkplumbingwg/helm-charts.git"}


build_dir="/tmp/sriov-network-operator-build"

src_dir=$(pwd)

git clone -b gh-pages $remote_url $build_dir

# Drop worktree on exit
trap "echo 'Removing Git repo $build_dir'; rm -rf '$build_dir'" EXIT

# Update Helm package index
mv $chart $build_dir/release
cd $build_dir/release
helm repo index . --url https://k8snetworkplumbingwg.github.io/helm-charts/release --merge ./index.yaml

# Commit change
commit_msg="Release SR-IOV Network Operator $release"
git add .
git commit -S -m "$commit_msg"
git push "$push_remote" gh-pages

