#!/bin/sh
set -eu
umask 077

usage() {
    echo "Usage: install-systemd.sh controller [--binary PATH]" >&2
    echo "       install-systemd.sh worker --controller https://HOST --node ID [--pairing-code-file PATH] [--binary PATH]" >&2
    exit 2
}

die() {
    echo "install-systemd: $*" >&2
    exit 1
}

[ "$#" -ge 1 ] || usage
role=$1
shift
case "$role" in controller|worker) ;; *) usage ;; esac
binary=
controller_url=
node=
pairing_file=
while [ "$#" -gt 0 ]; do
    [ "$#" -ge 2 ] || usage
    case "$1" in
        --binary) binary=$2 ;;
        --controller) controller_url=$2 ;;
        --node) node=$2 ;;
        --pairing-code-file) pairing_file=$2 ;;
        *) usage ;;
    esac
    shift 2
done

[ "$(id -u)" -eq 0 ] || die "run as root on the target systemd host"
[ "$(uname -s)" = Linux ] || die "systemd deployment requires Linux"
[ -d /run/systemd/system ] || die "systemd is not the active service manager"
for tool in install useradd usermod getent runuser systemctl; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
[ -d /etc/systemd/system ] || die "systemd unit directory not found"

name=project-workspace-$role
account=$name
state=/var/lib/$name
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
unit=$root/scripts/systemd/$name.service
[ -f "$unit" ] || die "missing service template: $unit"

if [ "$role" = worker ]; then
    [ -n "$controller_url" ] && [ -n "$node" ] || usage
    printf '%s\n' "$controller_url" |
        grep -Eq '^https://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$' ||
        die "worker controller URL must be an HTTPS origin without path or credentials"
    printf '%s\n' "$node" | grep -Eq '^[a-z][a-z0-9-]{1,62}$' ||
        die "worker node ID is invalid"
    getent group docker >/dev/null || die "Docker group is required on worker hosts"
    if [ -n "$pairing_file" ]; then
        [ -f "$pairing_file" ] && [ ! -L "$pairing_file" ] ||
            die "pairing code must be a regular file, not a symlink"
        case "$(stat -c %a -- "$pairing_file")" in
            400|600) ;;
            *) die "pairing code file must have mode 0400 or 0600" ;;
        esac
    fi
elif [ -n "$controller_url$node$pairing_file" ]; then
    usage
fi

build_dir=
if [ -z "$binary" ]; then
    command -v go >/dev/null 2>&1 ||
        die "Go is required to build; provide --binary PATH for a prebuilt executable"
    build_dir=$(mktemp -d)
    trap 'rm -rf "$build_dir"' EXIT HUP INT TERM
    (cd "$root" && CGO_ENABLED=1 go build -trimpath -o "$build_dir/$name" "./cmd/$name")
    binary=$build_dir/$name
fi
[ -f "$binary" ] && [ -x "$binary" ] || die "executable not found: $binary"

ensure_account() {
    if id -u "$account" >/dev/null 2>&1; then
        actual_home=$(getent passwd "$account" | cut -d: -f6)
        [ "$actual_home" = "$state" ] ||
            die "existing $account account uses an unexpected home directory"
    else
        useradd --system --user-group --home-dir "$state" --shell /usr/sbin/nologin "$account"
    fi
    install -d -m 0700 -o "$account" -g "$account" "$state"
}

ensure_account
if [ "$role" = worker ]; then
    usermod -aG docker "$account"
    if ! runuser -u "$account" -- env HOME="$state" docker info --format "{{.ServerVersion}}" >/dev/null 2>&1; then
        die "worker account cannot reach the Docker daemon; check Docker service and group access"
    fi
    token=$state/.config/project-workspace/workers/$node.token
    config=/etc/project-workspace/worker.env
    if [ -e "$config" ]; then
        [ "$(grep -c '^PWM_CONTROLLER_URL=' "$config" || true)" -eq 1 ] &&
        [ "$(grep -c '^PWM_NODE_ID=' "$config" || true)" -eq 1 ] &&
        grep -Fxq "PWM_CONTROLLER_URL=$controller_url" "$config" &&
        grep -Fxq "PWM_NODE_ID=$node" "$config" ||
            die "worker.env targets a different controller or node; review it before replacing"
    fi
    if [ ! -s "$token" ] && [ -z "$pairing_file" ]; then
        die "worker is not paired; provide --pairing-code-file with a controller-issued code"
    fi
fi

install -d -m 0700 /etc/project-workspace
target=/usr/local/bin/$name
install -d -m 0755 /usr/local/bin
if [ "$(readlink -f "$binary")" != "$(readlink -f "$target")" ]; then
    staged=$target.install.$$
    trap 'rm -f "$staged"; if [ -n "$build_dir" ]; then rm -rf "$build_dir"; fi' EXIT HUP INT TERM
    install -m 0755 "$binary" "$staged"
    mv -f "$staged" "$target"
fi

if [ "$role" = controller ]; then
    if [ ! -e "$state/.config/project-workspace/admin.token" ]; then
        runuser -u "$account" -- env HOME="$state" PWM_DB="$state/state.db" "$target" init
    fi
    if [ ! -e /etc/project-workspace/controller.env ]; then
        install -m 0600 /dev/null /etc/project-workspace/controller.env
    fi
else
    if [ ! -s "$token" ]; then
        runuser -u "$account" -- env HOME="$state" "$target" join \
            --backend docker --controller "$controller_url" --node "$node" --code-stdin < "$pairing_file"
    fi
    if [ ! -e "$config" ]; then
        printf 'PWM_CONTROLLER_URL=%s\nPWM_NODE_ID=%s\n' "$controller_url" "$node" > "$config"
        chmod 0600 "$config"
    fi
fi

install -m 0644 "$unit" "/etc/systemd/system/$name.service"
systemctl daemon-reload
systemctl enable "$name.service"
systemctl restart "$name.service"
systemctl is-active --quiet "$name.service" ||
    die "$name.service did not start; inspect journalctl -u $name.service"
printf 'Installed and started %s.service\n' "$name"
