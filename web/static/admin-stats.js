// Stats page: loads /api/admin/stats for the selected lab and period, then
// fills the "right now" line, the period figures, the activity chart and the
// labs table.
(function() {
    var page = document.getElementById('stats-page');
    if (!page) return;
    var project = page.dataset.project || '__all__';
    var range = page.dataset.range || 'all';

    var PERIOD_HEADINGS = {
        '7d': 'In the last 7 days',
        '1m': 'In the last month',
        '3m': 'In the last 3 months',
        '6m': 'In the last 6 months',
        '1y': 'In the last year',
        'all': 'Since the beginning'
    };
    var STATUS_LABELS = {
        completed: 'Running',
        failed: 'Failed',
        destroyed: 'Destroyed',
        removed: 'Removed'
    };

    // The filters apply as soon as one of them changes.
    var filters = document.getElementById('stats-filters');
    filters.addEventListener('change', function() { filters.submit(); });

    function show(id) { document.getElementById(id).hidden = false; }
    function hide(id) { document.getElementById(id).hidden = true; }
    function setText(id, text) { document.getElementById(id).textContent = text; }

    function plural(count, singular, pluralForm) {
        return count === 1 ? singular : pluralForm;
    }

    // append adds text to el, wrapping it in <strong> when emphasized.
    function append(el, text, emphasized) {
        if (emphasized) {
            var strong = document.createElement('strong');
            strong.textContent = text;
            el.appendChild(strong);
        } else {
            el.appendChild(document.createTextNode(text));
        }
    }

    function renderNow(data) {
        var line = document.getElementById('stats-now-line');
        line.textContent = '';
        if (data.running_labs === 0) {
            append(line, project === '__all__' ? 'No lab is running.' : 'This lab is not running.');
        } else {
            append(line, String(data.running_labs), true);
            append(line, ' ' + plural(data.running_labs, 'lab is', 'labs are') + ' running, with ');
            append(line, String(data.open_workspaces), true);
            append(line, ' ' + plural(data.open_workspaces, 'workspace', 'workspaces') + ' open.');
        }
        if (data.failed_labs > 0) {
            setText('stats-now-alert', data.failed_labs + ' ' +
                plural(data.failed_labs, 'lab', 'labs') +
                ' failed to deploy. Open ' + plural(data.failed_labs, 'it', 'them') +
                ' from the table below to retry or remove ' + plural(data.failed_labs, 'it', 'them') + '.');
            show('stats-now-alert');
        }
        show('stats-now');
    }

    function renderFigures(data) {
        setText('stats-period-heading', PERIOD_HEADINGS[range] || PERIOD_HEADINGS.all);
        setText('stat-students', data.students);
        setText('stat-workspaces', data.total_workspaces);
        setText('stat-labs', data.labs_deployed);
        if (data.feedback_count > 0) {
            setText('stat-rating', data.avg_rating.toFixed(1) + ' / 5');
            setText('stat-rating-help', 'From ' + data.feedback_count + ' ' +
                plural(data.feedback_count, 'student answer', 'student answers'));
        }
        show('stats-period');
    }

    // Bucket keys are "YYYY-MM-DD" (day) or "YYYY-MM" (month). They are handled
    // as UTC dates so stepping through them never trips on a DST change.
    function parseKey(key) {
        var parts = key.split('-').map(Number);
        return new Date(Date.UTC(parts[0], parts[1] - 1, parts[2] || 1));
    }

    function formatKey(date, granularity) {
        var key = date.getUTCFullYear() + '-' + String(date.getUTCMonth() + 1).padStart(2, '0');
        if (granularity === 'day') {
            key += '-' + String(date.getUTCDate()).padStart(2, '0');
        }
        return key;
    }

    function todayKey(granularity) {
        var now = new Date();
        return formatKey(new Date(Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())), granularity);
    }

    // fillSeries returns one entry per day/month from the start of the period
    // to today, so a period without activity shows as an empty slot instead of
    // being skipped.
    function fillSeries(data) {
        var granularity = data.granularity;
        var counts = {};
        (data.labels || []).forEach(function(label, i) { counts[label] = data.created[i]; });

        var first = data.since || (data.labels && data.labels[0]);
        if (!first) return { keys: [], values: [] };
        var last = todayKey(granularity);
        if (data.labels && data.labels.length > 0 && data.labels[data.labels.length - 1] > last) {
            last = data.labels[data.labels.length - 1];
        }

        var keys = [];
        var values = [];
        var cursor = parseKey(first);
        var end = parseKey(last);
        while (cursor <= end) {
            var key = formatKey(cursor, granularity);
            keys.push(key);
            values.push(counts[key] || 0);
            if (granularity === 'day') {
                cursor.setUTCDate(cursor.getUTCDate() + 1);
            } else {
                cursor.setUTCMonth(cursor.getUTCMonth() + 1);
            }
        }
        return { keys: keys, values: values };
    }

    function axisLabel(key, granularity) {
        var options = granularity === 'day'
            ? { day: 'numeric', month: 'short', timeZone: 'UTC' }
            : { month: 'short', year: 'numeric', timeZone: 'UTC' };
        return parseKey(key).toLocaleDateString('en-GB', options);
    }

    function renderChart(data) {
        var perDay = data.granularity === 'day';
        setText('stats-chart-title', 'Workspaces opened per ' + (perDay ? 'day' : 'month'));

        var series = fillSeries(data);
        if (data.total_workspaces === 0 || series.keys.length === 0 || typeof Chart === 'undefined') {
            hide('stats-chart-canvas');
            show('stats-chart-empty');
            return;
        }

        var styles = getComputedStyle(document.documentElement);
        var barColor = styles.getPropertyValue('--primary-color').trim() || '#2563eb';
        var gridColor = styles.getPropertyValue('--border').trim() || '#e2e8f0';
        var textColor = styles.getPropertyValue('--text-light').trim() || '#64748b';

        new Chart(document.getElementById('statsChart').getContext('2d'), {
            type: 'bar',
            data: {
                labels: series.keys.map(function(key) { return axisLabel(key, data.granularity); }),
                datasets: [{
                    data: series.values,
                    backgroundColor: barColor,
                    borderRadius: 4,
                    maxBarThickness: 28
                }]
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                plugins: {
                    legend: { display: false },
                    tooltip: {
                        displayColors: false,
                        callbacks: {
                            label: function(item) {
                                return item.parsed.y + ' ' + plural(item.parsed.y, 'workspace', 'workspaces') + ' opened';
                            }
                        }
                    }
                },
                scales: {
                    x: {
                        grid: { display: false },
                        border: { color: gridColor },
                        ticks: { color: textColor, maxRotation: 0, autoSkipPadding: 12 }
                    },
                    y: {
                        beginAtZero: true,
                        grid: { color: gridColor },
                        border: { display: false },
                        ticks: { color: textColor, precision: 0, maxTicksLimit: 5 }
                    }
                }
            }
        });
    }

    function cell(row, text, className) {
        var td = document.createElement('td');
        if (className) td.className = className;
        if (text !== undefined) td.textContent = text;
        row.appendChild(td);
        return td;
    }

    function link(href, text) {
        var a = document.createElement('a');
        a.href = href;
        a.textContent = text;
        return a;
    }

    function renderLabs(data) {
        var labs = data.labs || [];
        show('stats-labs');
        if (labs.length === 0) {
            hide('stats-labs-table');
            hide('stats-labs-note');
            show('stats-labs-empty');
            return;
        }
        setText('stats-labs-note', range === 'all'
            ? 'Open now is the current count. A removed lab keeps its workspace count, but its students are no longer known.'
            : 'Students, workspaces opened and rating cover the selected period. Open now is the current count.');

        var tbody = document.getElementById('stats-labs-body');
        tbody.textContent = '';
        labs.forEach(function(lab) {
            var removed = lab.status === 'removed';
            var tr = document.createElement('tr');

            var nameCell = cell(tr);
            nameCell.appendChild(lab.id ? link('/labs/' + encodeURIComponent(lab.id), lab.name) : document.createTextNode(lab.name));

            var statusCell = cell(tr);
            var badge = document.createElement('span');
            badge.className = 'stats-status stats-status-' + lab.status;
            badge.textContent = STATUS_LABELS[lab.status] || lab.status;
            statusCell.appendChild(badge);

            cell(tr, lab.created_at || '–', 'stats-col-date');
            cell(tr, removed ? '–' : lab.students, 'stats-col-num');
            cell(tr, lab.workspaces, 'stats-col-num');
            cell(tr, lab.status === 'completed' ? lab.open_now : '–', 'stats-col-num');

            var ratingCell = cell(tr, undefined, 'stats-col-num');
            if (lab.feedback_count > 0) {
                ratingCell.appendChild(link(
                    '/admin/feedback?lab_id=' + encodeURIComponent(lab.id),
                    lab.avg_rating.toFixed(1) + ' (' + lab.feedback_count + ')'));
            } else {
                ratingCell.textContent = '–';
            }
            tbody.appendChild(tr);
        });
    }

    fetch('/api/admin/stats?project=' + encodeURIComponent(project) + '&range=' + encodeURIComponent(range))
        .then(function(r) {
            if (!r.ok) throw new Error('stats request failed with status ' + r.status);
            return r.json();
        })
        .then(function(data) {
            hide('stats-loading');
            renderNow(data);
            renderFigures(data);
            renderChart(data);
            renderLabs(data);
        })
        .catch(function(err) {
            console.error('Failed to load stats:', err);
            hide('stats-loading');
            show('stats-error');
        });
})();
