#!/usr/bin/env python3
"""
patch_observe.py - CPA Control Panel (Observe Group) Injector Patch

This script patches `management.html` (the official CPA management control panel)
to:
1. Elevate the `cpa-usage` Dashboard into the primary 'Observe (观测)' navigation group as a first-class route (/usage).
2. Register the /usage route in React Router to directly render the plugin iframe.
3. Automatically handle plugin metadata fallback and eliminate any loading blockers.
"""

import sys
import os
import shutil
import argparse
import urllib.request

DEFAULT_SEARCH_PATHS = [
    "CLIProxyAPI/static/management.html",
    "static/management.html",
    "management.html",
    "../management.html",
    "../CLIProxyAPI/static/management.html",
]

TARGET_OBSERVE_PATTERN = "{id:`observe`,labelKey:`nav_groups.observe`,items:["
INJECTED_ITEM = (
    "{path:`/usage`,"
    "label:`使用量与计费`,"
    "meta:`模型调用与费用监控看板`,"
    "icon:K9.dashboard},"
)

def find_management_html(custom_path=None):
    if custom_path:
        if os.path.exists(custom_path):
            return os.path.abspath(custom_path)
        print(f"[!] Specified file not found: {custom_path}")
        return None

    for p in DEFAULT_SEARCH_PATHS:
        if os.path.exists(p):
            return os.path.abspath(p)
    return None

def download_management_html(dest_path="management.html"):
    url = "https://cpamc.router-for.me/"
    print(f"[*] Downloading latest management.html from {url}...")
    req = urllib.request.Request(url, headers={"User-Agent": "CLIProxyAPI-cpa-usage-installer"})
    with urllib.request.urlopen(req, timeout=30) as resp, open(dest_path, "wb") as f:
        shutil.copyfileobj(resp, f)
    print(f"[+] Downloaded successfully: {dest_path}")
    return os.path.abspath(dest_path)

def apply_patch(file_path):
    with open(file_path, "r", encoding="utf-8") as f:
        content = f.read()

    # Backup original
    bak_path = file_path + ".bak"
    if not os.path.exists(bak_path):
        shutil.copy2(file_path, bak_path)
        print(f"[*] Created backup: {bak_path}")

    modified = False

    # 1. Inject /usage item into Observe group
    if "path:`/usage`" not in content and "path:'/usage'" not in content:
        # Check if legacy item exists
        if "path:`/plugin-pages/cpa-usage/0`" in content:
            content = content.replace("path:`/plugin-pages/cpa-usage/0`", "path:`/usage`", 1)
            modified = True
            print("[+] Updated Observe Group item to first-class /usage route.")
        else:
            pos = content.find(TARGET_OBSERVE_PATTERN)
            if pos != -1:
                split_pos = pos + len(TARGET_OBSERVE_PATTERN)
                content = content[:split_pos] + INJECTED_ITEM + content[split_pos:]
                modified = True
                print("[+] Injected Usage Dashboard into Observe Group.")
            else:
                print("[!] Could not locate Observe group pattern.")

    # 2. Register first-class /usage route in React Router
    old_route = "{path:`/quota`,element:(0,V.jsx)(EN,{})},"
    new_route = "{path:`/quota`,element:(0,V.jsx)(EN,{})},{path:`/usage`,element:(0,V.jsx)(jN,{})},"
    if old_route in content and "{path:`/usage`" not in content:
        content = content.replace(old_route, new_route, 1)
        modified = True
        print("[+] Registered /usage route in React Router.")

    # 3. Update jN params fallback
    old_params = "d=(0,y.useMemo)(()=>kN(t.pluginId),[t.pluginId]),f=(0,y.useMemo)(()=>AN(t.menuIndex),[t.menuIndex])"
    new_params = "d=(0,y.useMemo)(()=>t.pluginId?kN(t.pluginId):`cpa-usage`,[t.pluginId]),f=(0,y.useMemo)(()=>t.menuIndex?AN(t.menuIndex):0,[t.menuIndex])"
    if old_params in content:
        content = content.replace(old_params, new_params, 1)
        modified = True
        print("[+] Injected /usage param fallback in jN.")

    # 4. Fallback for cpa-usage iframe rendering
    old_m = "let m=(0,y.useMemo)(()=>SM(i?.plugins??[]).find(e=>e.pluginID===d&&e.menuIndex===f),[i?.plugins,f,d]),h=m?pM(m.menu.path,r):``;"
    new_m = "let m=(0,y.useMemo)(()=>SM(i?.plugins??[]).find(e=>e.pluginID===d&&e.menuIndex===f)||(d===`cpa-usage`?{pluginID:`cpa-usage`,label:`使用量与计费`,menu:{path:`/v0/resource/plugins/cpa-usage/dashboard`}}:null),[i?.plugins,f,d]),h=m?pM(m.menu.path,r):``;"
    if old_m in content:
        content = content.replace(old_m, new_m, 1)
        modified = True
        print("[+] Injected cpa-usage metadata fallback in jN.")

    # 5. Bypass loading/unavailable guards for cpa-usage iframe
    old_shell = "children:o?(0,V.jsx)(`div`,{className:DN.stateShell,children:(0,V.jsx)(`div`,{className:DN.statusPanel,children:e(`common.loading`)})}):c?(0,V.jsx)(`div`,{className:DN.stateShell,children:(0,V.jsx)(gD,{title:e(`plugin_resource.unavailable`),description:c})}):m?h?(0,V.jsx)(`iframe`"
    new_shell = "children:d!==`cpa-usage`&&o?(0,V.jsx)(`div`,{className:DN.stateShell,children:(0,V.jsx)(`div`,{className:DN.statusPanel,children:e(`common.loading`)})}):d!==`cpa-usage`&&c?(0,V.jsx)(`div`,{className:DN.stateShell,children:(0,V.jsx)(gD,{title:e(`plugin_resource.unavailable`),description:c})}):m?h?(0,V.jsx)(`iframe`"
    if old_shell in content:
        content = content.replace(old_shell, new_shell, 1)
        modified = True
        print("[+] Bypassed loading/unavailable guards for cpa-usage iframe.")

    # 6. Activate plugin routes unconditionally
    old_router = "function one({location:e}){return Er(ane(Om(e=>e.supportsPlugin)),e)}"
    new_router = "function one({location:e}){return Er(ane(!0),e)}"
    if old_router in content:
        content = content.replace(old_router, new_router, 1)
        modified = True
        print("[+] Activated React Router plugin routes unconditionally.")

    # 7. Enable supportsPlugin by default in auth store
    old_store = "supportsPlugin:!1,connectionStatus:`disconnected`"
    new_store = "supportsPlugin:!0,connectionStatus:`disconnected`"
    if old_store in content:
        content = content.replace(old_store, new_store, 1)
        modified = True
        print("[+] Set supportsPlugin store default to true.")

    # 8. Enable sidebar plugin menu display
    old_side = "a=Om(e=>e.apiBase),o=Om(e=>e.supportsPlugin)"
    new_side = "a=Om(e=>e.apiBase),o=!0"
    if old_side in content:
        content = content.replace(old_side, new_side, 1)
        modified = True
        print("[+] Enabled sidebar plugin navigation unconditionally.")

    if modified:
        with open(file_path, "w", encoding="utf-8") as f:
            f.write(content)
        print(f"[SUCCESS] Deep patch applied to: {file_path}")
    else:
        print(f"[OK] {file_path} is already fully patched.")
    return True

def restore_patch(file_path):
    bak_path = file_path + ".bak"
    if not os.path.exists(bak_path):
        print(f"[!] Backup file not found: {bak_path}")
        return False
    shutil.copy2(bak_path, file_path)
    print(f"[+] Restored original from: {bak_path}")
    return True

def main():
    parser = argparse.ArgumentParser(description="Inject cpa-usage into CPA Management Observe Group")
    parser.add_argument("--html", help="Path to management.html")
    parser.add_argument("--fetch", action="store_true", help="Download management.html if not found locally")
    parser.add_argument("--restore", action="store_true", help="Restore management.html from backup")
    args = parser.parse_args()

    target_file = find_management_html(args.html)
    if not target_file:
        if args.fetch:
            target_file = download_management_html()
        else:
            print("[!] management.html not found. Use --fetch to download it automatically, or specify --html <path>")
            sys.exit(1)

    if args.restore:
        success = restore_patch(target_file)
    else:
        success = apply_patch(target_file)

    sys.exit(0 if success else 1)

if __name__ == "__main__":
    main()
