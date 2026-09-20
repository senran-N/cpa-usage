#!/usr/bin/env bash
set -e

echo "========================================================"
echo "   CPA Usage & Billing Plugin Setup (Linux / macOS)"
echo "========================================================"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CPA_USAGE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
CPA_DIR="$(cd "${CPA_USAGE_DIR}/../CLIProxyAPI" && pwd || true)"

PLUGINS_DIR="${CPA_DIR}/plugins"
mkdir -p "${PLUGINS_DIR}"

# Detect OS and library name
OS="$(uname -s)"
if [ "$OS" = "Darwin" ]; then
    LIB_EXT="dylib"
else
    LIB_EXT="so"
fi

echo "[*] Target plugins directory: ${PLUGINS_DIR}"

if [ -f "${CPA_USAGE_DIR}/cpa_usage.${LIB_EXT}" ]; then
    echo "[*] Copying cpa_usage.${LIB_EXT} to plugins/cpa-usage.${LIB_EXT}..."
    cp -f "${CPA_USAGE_DIR}/cpa_usage.${LIB_EXT}" "${PLUGINS_DIR}/cpa-usage.${LIB_EXT}"
    echo "[OK] Plugin library deployed."
elif [ -f "${CPA_USAGE_DIR}/cpa_usage.dll" ]; then
    echo "[*] Copying cpa_usage.dll to plugins/cpa-usage.dll..."
    cp -f "${CPA_USAGE_DIR}/cpa_usage.dll" "${PLUGINS_DIR}/cpa-usage.dll"
    echo "[OK] Plugin DLL deployed."
else
    echo "[!] Plugin binary not found. Building now..."
    cd "${CPA_USAGE_DIR}"
    go build -buildmode=c-shared -o "cpa_usage.${LIB_EXT}" .
    cp -f "${CPA_USAGE_DIR}/cpa_usage.${LIB_EXT}" "${PLUGINS_DIR}/cpa-usage.${LIB_EXT}"
    echo "[OK] Built and deployed cpa-usage.${LIB_EXT}"
fi

echo "[*] Applying Observe Group patch to management.html..."
python3 "${SCRIPT_DIR}/patch_observe.py" --fetch || python "${SCRIPT_DIR}/patch_observe.py" --fetch

echo ""
echo "========================================================"
echo "Recommended config.yaml configuration for CPA:"
echo "========================================================"
cat << 'EOF'
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-usage:
      enabled: true
      db_path: "data/cpa_usage.db"
      retention_days: 90
EOF
echo "========================================================"
echo ""
echo "Setup completed! Start or restart CLIProxyAPI to use."
