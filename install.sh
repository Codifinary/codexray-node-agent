#!/bin/sh
set -e

GITHUB_URL="https://github.com/codifinary/codexray-node-agent/releases"
DOWNLOADER=
SUDO=sudo
if [ $(id -u) -eq 0 ]; then
    SUDO=
fi

BIN_DIR=/usr/bin
SYSTEMD_DIR=/etc/systemd/system
VERSION="latest"
SYSTEM_NAME=codexray-node-agent
SYSTEMD_SERVICE=${SYSTEM_NAME}.service
UNINSTALL_SH=${BIN_DIR}/${SYSTEM_NAME}-uninstall.sh
FILE_SERVICE=${SYSTEMD_DIR}/${SYSTEMD_SERVICE}
FILE_ENV=${SYSTEMD_DIR}/${SYSTEMD_SERVICE}.env
STATE_DIR=/var/lib/codexray-node-agent
BINARY=
# Empty: 2G on a fresh install, the previous unit's value on an upgrade (see unit_defaults).
MEMORY_MAX=${MEMORY_MAX:-}
# Agent settings copied from the installer's environment into ${FILE_ENV}. Matched as
# prefixes of NAME=value, so e.g. STATSD_ covers every STATSD_* setting.
ENV_VARS="^(LISTEN|CGROUPFS_ROOT|DISABLE_LOG_PARSING|DISABLE_PINGER|DISABLE_L7_TRACING|TRACK_PUBLIC_NETWORK|EPHEMERAL_PORT_RANGE|PROVIDER|REGION|AVAILABILITY_ZONE|INSTANCE_TYPE|INSTANCE_LIFE_CYCLE|LOG_PER_SECOND|LOG_BURST|COLLECTOR_ENDPOINT|API_KEY|METRICS_ENDPOINT|TRACES_ENDPOINT|LOGS_ENDPOINT|PROFILES_ENDPOINT|SCRAPE_INTERVAL|WAL_DIR|INSECURE_SKIP_VERIFY|NODE_METRICS_ENABLED|CONTAINER_|EBPF_L7_|PROFILING_|TRACES_|GO_MEMORY_LIMIT_RATIO|GOMEMLIMIT|MEMORY_WATCHDOG_|PPROF_|STATSD_|NODE_FS_|DNS_MAX_DOMAINS_PER_CONTAINER|EXCLUDE_HTTP_REQUESTS_BY_PATH|INSTRUMENTATION_DELAY|LOG_PATTERNS_MAX|MAX_LABEL_LENGTH|MAX_SPOOL_SIZE|MIN_CONTAINER_AGE)"

info()
{
    echo '[INFO] ' "$@"
}

fatal()
{
    echo '[ERROR] ' "$@" >&2
    show_help
    exit 1
}

show_help() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  -h, --help                        Show this help message and exit"
    echo "  -v v1.22.2, --version v1.22.2     Specify the version to install (default: latest)"
    echo "  -b PATH, --binary PATH            Install this local binary instead of downloading a release"
    echo "  -m 2G, --memory-max 2G            systemd MemoryMax for the agent (default: 2G on a fresh install;"
    echo "                                    on upgrade, the existing unit's value, or none; 'infinity' for no cap)"
    echo ""
    echo "Agent settings (COLLECTOR_ENDPOINT, API_KEY, CONTAINER_ALLOWLIST, EBPF_L7_TRACES_ENABLED, ...) are"
    echo "read from the environment, e.g.:"
    echo "  sudo COLLECTOR_ENDPOINT=http://host:8182/ingest API_KEY=... \\"
    echo "       CONTAINER_ALLOWLIST='^/system.slice/(nginx|postgresql)[.]service\$' sh install.sh"
    echo "CONTAINER_ALLOWLIST is required (since v1.4.0): one regex on the cgroup path of the services or"
    echo "containers to monitor; use alternation (a|b) for several, and [.] rather than a backslash."
    echo "On upgrade, settings already in ${FILE_ENV} are kept unless set again, and so are the"
    echo "existing unit's MemoryMax, LISTEN and WAL_DIR."
}

verify_system() {
    if [ -x /bin/systemctl ] || type systemctl > /dev/null 2>&1; then
        return
    fi
    fatal 'Cannot find systemd'
}

verify_executable() {
    if [ ! -x ${BIN_DIR}/codexray-node-agent ]; then
        fatal "Executable codexray-node-agent binary not found at ${BIN_DIR}/codexray-node-agent"
    fi
}

verify_arch() {
    if [ -z "$ARCH" ]; then
        ARCH=$(uname -m)
    fi
    case $ARCH in
        amd64)
            ARCH=amd64
            ;;
        x86_64)
            ARCH=amd64
            ;;
        arm64)
            ARCH=arm64
            ;;
        aarch64)
            ARCH=arm64
            ;;
        *)
            fatal "Unsupported architecture $ARCH"
    esac
}

verify_downloader() {
    [ -x "$(command -v $1)" ] || return 1
    DOWNLOADER=$1
    return 0
}

setup_tmp() {
    TMP_DIR=$(mktemp -d -t codexray-agent-install.XXXXXXXXXX)
    TMP_BIN=${TMP_DIR}/codexray-node-agent
    cleanup() {
        code=$?
        set +e
        trap - EXIT
        rm -rf ${TMP_DIR}
        exit $code
    }
    trap cleanup INT EXIT
}

get_release_version() {
    if [ "$VERSION" = "latest" ]; then
        info "Finding the latest release"
        latest_release_url=${GITHUB_URL}/latest
        case $DOWNLOADER in
            curl)
                VERSION=$(curl -w '%{url_effective}' -L -s -S ${latest_release_url} -o /dev/null | sed -e 's|.*/||')
                ;;
            wget)
                VERSION=$(wget -SqO /dev/null ${latest_release_url} 2>&1 | grep -i Location | sed -e 's|.*/||')
                ;;
            *)
                fatal "Incorrect downloader executable '$DOWNLOADER'"
                ;;
        esac
        info "The latest release is ${VERSION}"
    else
        info "Using specified version ${VERSION}"
    fi
}

download_binary() {
    info "Downloading binary"
    URL="${GITHUB_URL}/download/${VERSION}/codexray-node-agent-${ARCH}"
    set +e
    case $DOWNLOADER in
        curl)
            curl -o ${TMP_BIN} -sfL ${URL}
            ;;
        wget)
            wget -qO ${TMP_BIN} ${URL}
            ;;
        *)
            fatal "Incorrect executable '$DOWNLOADER'"
            ;;
    esac

    [ $? -eq 0 ] || fatal 'Download failed'
    set -e
}

setup_binary() {
    chmod 755 ${TMP_BIN}
    info "Installing codexray-node-agent to ${BIN_DIR}/codexray-node-agent"
    $SUDO chown root:root ${TMP_BIN}
    $SUDO mv -f ${TMP_BIN} ${BIN_DIR}/codexray-node-agent
}

download() {
    if [ -n "$BINARY" ]; then
        [ -f "$BINARY" ] || fatal "Binary not found: $BINARY"
        setup_tmp
        cp "$BINARY" ${TMP_BIN}
        info "Using local binary $BINARY"
        setup_binary
        return
    fi
    verify_arch
    verify_downloader curl || verify_downloader wget || fatal 'Can not find curl or wget for downloading files'
    setup_tmp
    get_release_version
    download_binary
    setup_binary
}

create_uninstall() {
    info "Creating uninstall script ${UNINSTALL_SH}"
    $SUDO tee ${UNINSTALL_SH} >/dev/null << EOF
#!/bin/sh
set -x
[ \$(id -u) -eq 0 ] || exec sudo \$0 \$@

systemctl stop ${SYSTEM_NAME}
systemctl disable ${SYSTEM_NAME}
systemctl reset-failed ${SYSTEM_NAME}
systemctl daemon-reload

rm -f ${FILE_SERVICE}
rm -f ${FILE_ENV}

remove_uninstall() {
    rm -f ${UNINSTALL_SH}
}
trap remove_uninstall EXIT

rm -rf /var/lib/codexray-node-agent || true
rm -f ${BIN_DIR}/codexray-node-agent
EOF
    $SUDO chmod 755 ${UNINSTALL_SH}
    $SUDO chown root:root ${UNINSTALL_SH}
}

# unit_defaults decides MemoryMax, LISTEN and WAL_DIR for the unit. A fresh install gets the
# recommended values (2G cap, :10300, ${STATE_DIR}). An upgrade keeps what the existing unit
# had: units written by earlier installers had no cap and no LISTEN/WAL_DIR lines (the agent
# then defaults to 0.0.0.0:80 and /tmp/Codexray-node-agent), and silently changing them would
# OOM-kill busy hosts, move the port, or orphan the unsent spool.
unit_defaults() {
    if [ -f "${FILE_SERVICE}" ]; then
        prev_unit_value() { $SUDO sed -n "s/^$1//p" ${FILE_SERVICE} | tail -n 1; }
        PREV_MEMORY_MAX=$(prev_unit_value 'MemoryMax=')
        UNIT_LISTEN=$(prev_unit_value 'Environment=LISTEN=')
        UNIT_WAL_DIR=$(prev_unit_value 'Environment=WAL_DIR=')
        [ -n "${MEMORY_MAX}" ] || MEMORY_MAX=${PREV_MEMORY_MAX}
        info "upgrade: using MemoryMax=${MEMORY_MAX:-<none>} LISTEN=${UNIT_LISTEN:-<agent default>} WAL_DIR=${UNIT_WAL_DIR:-<agent default>}"
        [ -n "${MEMORY_MAX}" ] || info "upgrade: no memory cap; pass --memory-max 2G to add one (recommended)"
    else
        [ -n "${MEMORY_MAX}" ] || MEMORY_MAX=2G
        UNIT_LISTEN=0.0.0.0:10300
        UNIT_WAL_DIR=${STATE_DIR}
    fi
}

systemd_disable() {
    $SUDO systemctl disable ${SYSTEM_NAME} >/dev/null 2>&1 || true
    $SUDO rm -f ${FILE_SERVICE} || true
}

# Writes ${FILE_ENV}: settings exported to this script replace the same names in the
# existing file; everything else in the existing file is kept, so an upgrade does not
# drop the API key or endpoint. The file is root-only (0600) because it holds API_KEY.
# The agent exits at startup without a container allowlist (required since v1.4.0). Check before
# anything is changed, so a missing allowlist never leaves a stopped service or a unit that
# restarts forever. It may come from this run's environment or the existing env file.
verify_allowlist() {
    if [ -n "${CONTAINER_ALLOWLIST}" ]; then
        return
    fi
    if $SUDO grep -qE "^CONTAINER_ALLOWLIST=['\"]?[^'\"]" ${FILE_ENV} 2>/dev/null; then
        return
    fi
    fatal "CONTAINER_ALLOWLIST is required: set it to a regex for the services or containers to monitor, e.g. CONTAINER_ALLOWLIST='^/system.slice/(nginx|postgresql)[.]service\$'"
}

create_env_file() {
    info "env: Updating environment file ${FILE_ENV}"
    NEW_ENV=$(sh -c export | while read x v; do echo "$v"; done | grep -E "${ENV_VARS}" || true)
    OLD_ENV=$($SUDO cat ${FILE_ENV} 2>/dev/null || true)
    {
        echo "$OLD_ENV" | while IFS= read -r line; do
            [ -n "$line" ] || continue
            name=${line%%=*}
            echo "$NEW_ENV" | grep -q "^${name}=" || echo "$line"
        done
        [ -z "$NEW_ENV" ] || echo "$NEW_ENV"
    } | $SUDO sh -c "umask 077; cat > ${FILE_ENV}.tmp && mv -f ${FILE_ENV}.tmp ${FILE_ENV}"
    $SUDO chmod 0600 ${FILE_ENV}
}

# unit_lines prints the optional memory cap and LISTEN/WAL_DIR defaults chosen by unit_defaults.
unit_lines() {
    if [ -n "${MEMORY_MAX}" ]; then
        echo "# Memory cap. The agent sizes its Go soft limit and diagnostic watchdog from this"
        echo "# cgroup limit; without a cap neither takes effect. Override with --memory-max."
        echo "MemoryMax=${MEMORY_MAX}"
        echo "MemorySwapMax=0"
    fi
    echo "# Defaults; the EnvironmentFile entries above take precedence."
    [ -z "${UNIT_LISTEN}" ] || echo "Environment=LISTEN=${UNIT_LISTEN}"
    [ -z "${UNIT_WAL_DIR}" ] || echo "Environment=WAL_DIR=${UNIT_WAL_DIR}"
}

create_systemd_service_file() {
    info "systemd: Creating service file ${FILE_SERVICE}"
    $SUDO tee ${FILE_SERVICE} >/dev/null << EOF
[Unit]
Description=Codexray node agent
Documentation=https://codexray.io
Wants=network-online.target
After=network-online.target

[Install]
WantedBy=multi-user.target

[Service]
Type=exec
EnvironmentFile=-/etc/default/%N
EnvironmentFile=-/etc/sysconfig/%N
EnvironmentFile=-${FILE_ENV}
KillMode=process
Delegate=yes
# Having non-zero Limit*s causes performance problems due to accounting overhead
# in the kernel. We recommend using cgroups to do container-local accounting.
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
TimeoutStartSec=0
Restart=always
RestartSec=5s
$(unit_lines)
Environment=GO_MEMORY_LIMIT_RATIO=0.7
Environment=MEMORY_WATCHDOG_THRESHOLD_RATIO=0.8
Environment=PPROF_LISTEN=127.0.0.1:10302
StateDirectory=codexray-node-agent
ExecStart=${BIN_DIR}/codexray-node-agent
EOF
}

create_service_file() {
    create_systemd_service_file
    return 0
}

get_installed_hashes() {
    $SUDO sha256sum ${BIN_DIR}/codexray-node-agent ${FILE_SERVICE} ${FILE_ENV} 2>&1 || true
}

systemd_enable() {
    info "systemd: Enabling ${SYSTEM_NAME} unit"
    $SUDO systemctl enable ${FILE_SERVICE} >/dev/null
    $SUDO systemctl daemon-reload >/dev/null
}

systemd_start() {
    info "systemd: Starting ${SYSTEM_NAME}"
    $SUDO systemctl restart ${SYSTEM_NAME}
}

service_enable_and_start() {
    systemd_enable

    POST_INSTALL_HASHES=$(get_installed_hashes)
    if [ "${PRE_INSTALL_HASHES}" = "${POST_INSTALL_HASHES}" ]; then
        info 'No change detected so skipping service start'
        return
    fi

    systemd_start

    return 0
}

while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help)
            show_help
            exit 0
            ;;
        -v|--version)
            VERSION="$2"
            shift 2
            ;;
        -b|--binary)
            BINARY="$2"
            shift 2
            ;;
        -m|--memory-max)
            MEMORY_MAX="$2"
            shift 2
            ;;
        *)
            fatal "Unknown option: $1"
            ;;
    esac
done

{
    verify_system
    verify_allowlist
    unit_defaults
    download
    create_uninstall
    systemd_disable
    create_env_file
    create_service_file
    service_enable_and_start
}
