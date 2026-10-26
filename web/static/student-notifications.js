// Shows the messages a workshop admin sends — on the page, and as a desktop
// notification so the student sees them while they are in their IDE tab rather
// than here.
//
// This is JS rather than HTMX on purpose: the Notification API has no
// declarative equivalent, and My Workspaces does not load HTMX at all. The poll
// follows the same reasoning as the workspace poll in student-dashboard.js — a
// fixed interval makes every student in a workshop poll in lockstep, so the
// delay is jittered.

const NOTIFY_POLL_BASE_MS = 20000;
const NOTIFY_POLL_JITTER_MS = 5000;

// Ids already shown in this tab. sessionStorage rather than localStorage: a
// message is worth showing once per browsing session, and a tab opened later in
// the workshop should still catch what is up. Every access is guarded — it
// throws in a private window with site data blocked.
const NOTIFY_SEEN_KEY = 'easylab_seen_notifications';

let _notifyPollTimer = null;

function notifySeenIds() {
    try {
        const raw = sessionStorage.getItem(NOTIFY_SEEN_KEY);
        return raw ? new Set(JSON.parse(raw)) : new Set();
    } catch (e) {
        return new Set();
    }
}

function rememberNotifySeen(ids) {
    try {
        // Capped: a long workshop must not grow this without bound.
        sessionStorage.setItem(NOTIFY_SEEN_KEY, JSON.stringify(Array.from(ids).slice(-200)));
    } catch (e) {
        /* nothing to do: the banner still showed, it may just show again */
    }
}

// notificationsSupported reports whether this browser can raise a desktop
// notification at all. Served over plain HTTP (a lab without a domain) it is
// missing, and the in-page banner is then the only channel here.
function notificationsSupported() {
    return typeof window !== 'undefined' && 'Notification' in window;
}

// updateNotifyBell reflects the permission state on the bell button, so the
// student can tell whether desktop notifications will actually arrive.
function updateNotifyBell() {
    const bell = document.getElementById('notify-bell');
    if (!bell) return;

    if (!notificationsSupported()) {
        bell.classList.add('is-unavailable');
        bell.title = 'Desktop notifications are not available in this browser';
        bell.setAttribute('aria-label', bell.title);
        return;
    }

    bell.classList.remove('student-notify-bell--granted', 'student-notify-bell--denied');
    if (Notification.permission === 'granted') {
        bell.classList.add('student-notify-bell--granted');
        bell.title = 'Desktop notifications are on';
    } else if (Notification.permission === 'denied') {
        bell.classList.add('student-notify-bell--denied');
        bell.title = 'Desktop notifications are blocked for this site';
    } else {
        bell.title = 'Turn on desktop notifications';
    }
    bell.setAttribute('aria-label', bell.title);
}

// requestNotifyPermission asks on a click rather than on load: browsers want the
// prompt tied to a gesture, and an unexplained prompt the moment a page opens is
// the one students dismiss.
async function requestNotifyPermission() {
    if (!notificationsSupported()) return;
    try {
        await Notification.requestPermission();
    } catch (e) {
        console.error('Failed to request notification permission:', e);
    }
    updateNotifyBell();
}

// showDesktopNotification raises the OS-level notification. The id is the tag, so
// several open student tabs show one notification between them instead of one
// each.
function showDesktopNotification(notification) {
    if (!notificationsSupported() || Notification.permission !== 'granted') return;
    try {
        const title = notification.lab_name ? `EasyLab · ${notification.lab_name}` : 'EasyLab';
        new Notification(title, { body: notification.message, tag: notification.id });
    } catch (e) {
        console.error('Failed to show notification:', e);
    }
}

// renderNotificationBanner puts the message on the page itself, for the student
// who is looking at it, and takes it down again when its time is up.
function renderNotificationBanner(notification) {
    const container = document.getElementById('student-notifications');
    if (!container) return;

    const banner = document.createElement('div');
    banner.className = 'student-notification';
    if (notification.level === 'warning') {
        banner.classList.add('student-notification--warning');
    }
    banner.setAttribute('role', 'status');
    banner.dataset.notificationId = notification.id;

    const label = notification.lab_name ? `${notification.lab_name}: ` : '';
    banner.innerHTML = `
        <span class="student-notification-icon" aria-hidden="true">${notification.level === 'warning' ? '⚠️' : '📣'}</span>
        <span class="student-notification-text"><strong>${escapeHtml(label)}</strong>${escapeHtml(notification.message)}</span>
    `;
    container.appendChild(banner);
    container.style.display = '';

    const expiresAt = Date.parse(notification.expires_at);
    const remaining = Number.isNaN(expiresAt) ? 0 : expiresAt - Date.now();
    // A message with no time left is still worth a moment on screen: the student
    // may have opened the page just as it expired.
    setTimeout(() => {
        banner.remove();
        if (!container.children.length) {
            container.style.display = 'none';
        }
    }, Math.max(remaining, 8000));
}

async function pollNotifications() {
    try {
        const resp = await fetch('/api/student/notifications', { credentials: 'same-origin' });
        if (!resp.ok) return;
        const payload = await resp.json();
        const items = Array.isArray(payload.notifications) ? payload.notifications : [];

        const seen = notifySeenIds();
        let added = false;
        items.forEach(item => {
            if (!item || !item.id || seen.has(item.id)) return;
            seen.add(item.id);
            added = true;
            renderNotificationBanner(item);
            showDesktopNotification(item);
        });
        if (added) {
            rememberNotifySeen(seen);
        }
    } catch (e) {
        // A failed poll is not worth telling the student about; the next one may
        // well succeed.
        console.error('Failed to load notifications:', e);
    }
}

function scheduleNotifyPoll() {
    const delay = NOTIFY_POLL_BASE_MS + Math.random() * NOTIFY_POLL_JITTER_MS;
    _notifyPollTimer = setTimeout(async () => {
        await pollNotifications();
        scheduleNotifyPoll();
    }, delay);
}

document.addEventListener('DOMContentLoaded', () => {
    const bell = document.getElementById('notify-bell');
    if (bell) {
        bell.addEventListener('click', requestNotifyPermission);
    }
    updateNotifyBell();
    pollNotifications();
    scheduleNotifyPoll();
});
