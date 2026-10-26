package kube

import (
	"encoding/json"
	"fmt"
	"strings"

	"easylab/internal/providers/workspace"
)

// Students spend a workshop in their IDE tab, not on the portal, so an admin's
// message has to reach them there. code-server is served straight through the
// workspace's Ingress with no proxy and no path prefix of EasyLab's, so nothing
// can be injected into its page from outside: the message has to come from
// inside the container.
//
// This writes a two-file VS Code extension into the IDE's extensions directory
// as part of the same best-effort bootstrap that already seeds settings.json and
// installs extensions (see setupSteps). A plain folder with a package.json and a
// JS entry point is all an extension needs, so there is no build step, no .vsix
// to package and no registry to install from.
//
// The extension polls Spec.NotifyURL carrying Spec.NotifyToken and shows what
// comes back with code-server's own notifications. Everything it does is
// best-effort: a workshop must not break because a message endpoint is down.

const (
	// notifyExtensionDir is the extension's folder name under the IDE's extensions
	// directory.
	notifyExtensionDir = "easylab-notify"
	// envNotifyURL and envNotifyToken are how the workspace container is told
	// where to read its notifications and with what. The extension reads them
	// from its own process environment.
	envNotifyURL   = "EASYLAB_NOTIFY_URL"
	envNotifyToken = "EASYLAB_NOTIFY_TOKEN"
)

// notifyExtensionStep returns the bootstrap lines that install the in-IDE
// notification extension, or "" when the workspace has nowhere to poll.
//
// Unlike userSettingsStep this overwrites on every start: the files are
// EasyLab's, not the student's, so a workspace picks up a newer version of the
// extension — or a changed URL or token — simply by restarting.
func notifyExtensionStep(spec workspace.Spec, p ideProfile) string {
	url := strings.TrimSpace(spec.NotifyURL)
	if url == "" || !p.supportsExtensions {
		return ""
	}

	manifest, err := json.MarshalIndent(notifyExtensionManifest(), "", "  ")
	if err != nil {
		// Only a programming error could get here; a workspace must still start.
		return ""
	}

	var b strings.Builder
	b.WriteString("e=\"${XDG_DATA_HOME:-$HOME/.local/share}/code-server/extensions\"; ")
	b.WriteString("d=\"$e/" + notifyExtensionDir + "\"; ")
	b.WriteString("{ mkdir -p \"$d\" && ")
	fmt.Fprintf(&b, "printf '%%s\\n' %s > \"$d/package.json\" && ", shellQuote(string(manifest)))
	fmt.Fprintf(&b, "printf '%%s\\n' %s > \"$d/extension.js\" && ", shellQuote(notifyExtensionSource))
	// extensions.json is the IDE's cache of what it found in this directory last
	// time, and a folder dropped in behind its back is not in it. Removing it
	// before the IDE starts makes it scan the directory again, which is also how
	// the extensions a template installed are found.
	b.WriteString("rm -f \"$e/extensions.json\"")
	b.WriteString("; } || true\n")
	return b.String()
}

// notifyExtensionManifest is the extension's package.json. onStartupFinished
// activates it once the IDE is up without delaying that startup, and no
// contributions are declared because it has no commands or views of its own.
func notifyExtensionManifest() map[string]any {
	return map[string]any{
		"name":        notifyExtensionDir,
		"displayName": "EasyLab notifications",
		"description": "Shows the messages your workshop admin sends.",
		"version":     "1.0.0",
		"publisher":   "easylab",
		"engines":     map[string]any{"vscode": "^1.60.0"},
		// "*" alongside onStartupFinished so an older code-server that does not
		// know the newer event still activates the extension.
		"activationEvents": []string{"onStartupFinished", "*"},
		"main":             "./extension.js",
	}
}

// notifyExtensionSource is the extension itself. It runs in the extension host,
// which is Node, so it reads its configuration from the environment the
// container was given and uses Node's own http/https rather than a dependency.
//
// Deliberate properties:
//   - every failure is swallowed, including a missing URL or token: the student's
//     IDE must behave exactly as before when notifications are unavailable;
//   - ids already shown are remembered for the life of the window, so a message
//     is not shown again on every poll;
//   - the response body is capped, because the IDE is talking to a URL over the
//     network and must not grow its own memory on a bad answer.
const notifyExtensionSource = `const vscode = require('vscode');
const http = require('http');
const https = require('https');
const { URL } = require('url');

const POLL_MS = 20000;
const MAX_BYTES = 64 * 1024;

const seen = new Set();
let timer = null;

function fetchNotifications(endpoint) {
    return new Promise((resolve) => {
        let target;
        try {
            target = new URL(endpoint);
        } catch (e) {
            resolve(null);
            return;
        }
        const client = target.protocol === 'http:' ? http : https;
        const req = client.get(target, { timeout: 10000 }, (res) => {
            if (res.statusCode !== 200) {
                res.resume();
                resolve(null);
                return;
            }
            let body = '';
            res.setEncoding('utf8');
            res.on('data', (chunk) => {
                if (body.length > MAX_BYTES) {
                    req.destroy();
                    return;
                }
                body += chunk;
            });
            res.on('end', () => {
                try {
                    resolve(JSON.parse(body));
                } catch (e) {
                    resolve(null);
                }
            });
        });
        req.on('timeout', () => req.destroy());
        req.on('error', () => resolve(null));
    });
}

function show(notification) {
    const message = String(notification.message || '').slice(0, 500);
    if (!message) {
        return;
    }
    if (notification.level === 'warning') {
        vscode.window.showWarningMessage(message);
    } else {
        vscode.window.showInformationMessage(message);
    }
}

async function poll(endpoint) {
    const payload = await fetchNotifications(endpoint);
    const items = payload && Array.isArray(payload.notifications) ? payload.notifications : [];
    for (const item of items) {
        if (!item || !item.id || seen.has(item.id)) {
            continue;
        }
        seen.add(item.id);
        show(item);
    }
}

function activate() {
    const base = process.env.EASYLAB_NOTIFY_URL;
    const token = process.env.EASYLAB_NOTIFY_TOKEN;
    if (!base || !token) {
        return;
    }
    let endpoint;
    try {
        const url = new URL(base);
        url.searchParams.set('token', token);
        endpoint = url.toString();
    } catch (e) {
        return;
    }
    const tick = () => {
        poll(endpoint).catch(() => {}).then(() => {
            timer = setTimeout(tick, POLL_MS);
        });
    };
    tick();
}

function deactivate() {
    if (timer) {
        clearTimeout(timer);
        timer = null;
    }
}

module.exports = { activate, deactivate };
`
