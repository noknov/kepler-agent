#!/bin/sh
# Configure transport and diagnostics only; never install task dependencies.
set -eu
mirror=${1:-}
recommends=${2:-enabled}
mkdir -p /logs/agent
if command -v apt-get >/dev/null 2>&1; then
    case "$recommends" in
        disabled) printf 'APT::Install-Recommends "false";\n' > /etc/apt/apt.conf.d/99-kepler-eval-recommends ;;
        enabled) rm -f /etc/apt/apt.conf.d/99-kepler-eval-recommends ;;
        *) echo 'apt_recommends must be enabled or disabled' >&2; exit 1 ;;
    esac
    cat > /etc/apt/apt.conf.d/99-kepler-eval-logs <<'EOF'
Dir::Log::Terminal "/logs/agent/apt-term.log";
Dir::Log::History "/logs/agent/apt-history.log";
EOF
    if test -n "$mirror"; then
        # Match complete Ubuntu archive URIs in both legacy and deb822 sources.
        # Preserve suites, components, architectures, keys and third-party URIs.
        for source in /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
            test -f "$source" || continue
            awk -v mirror="$mirror" '{
                gsub(/https?:\/\/(archive\.ubuntu\.com|security\.ubuntu\.com)\/ubuntu\/?([[:space:]]|$)/, mirror "/ ")
                print
            }' "$source" > "$source.kepler-tmp"
            cat "$source.kepler-tmp" > "$source"
            rm "$source.kepler-tmp"
        done
        # Minimal images may have no system CA bundle. Use uploaded trusted
        # roots without disabling HTTPS verification or apt signatures.
        if test -s /tmp/kepler-eval-ca.pem; then
            host=${mirror#https://}
            host=${host%%/*}
            host=${host%%:*}
            printf 'Acquire::https::%s::CaInfo "/tmp/kepler-eval-ca.pem";\n' "$host" > /etc/apt/apt.conf.d/99-kepler-eval-ca
        fi
    fi
fi
