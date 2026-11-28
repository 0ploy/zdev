#!/bin/sh
# zdev's status line for Claude Code in a sandbox (also fine on a host):
# folder + sandbox marker + model + git branch + context + (subscription window | cost)
#
# All number formatting goes through jq: shell printf '%.0f' fails under a
# comma-decimal locale (LC_NUMERIC=de_DE -> "printf: 12.3: invalid number").

input=$(cat)

cwd=$(echo "$input" | jq -r '.workspace.current_dir // .cwd // ""')
# In a zdev sandbox the source sits at the service's mount path (/app):
# name the project's folder on the host instead.
folder=${ZDEV_HOST_PROJECTDIR:-$(basename "$cwd")}
model=$(echo "$input" | jq -r '.model.display_name // ""')
used=$(echo "$input" | jq -r '.context_window.used_percentage // empty | round')
limit_5h=$(echo "$input" | jq -r '.rate_limits.five_hour.used_percentage // empty | round')
limit_7d=$(echo "$input" | jq -r '.rate_limits.seven_day.used_percentage // empty | round')
reset_at=$(echo "$input" | jq -r '.rate_limits.five_hour.resets_at // empty')
cost=$(echo "$input" | jq -r '.cost.total_cost_usd // empty | select(. > 0) | . * 100 | round / 100 | tostring')

branch=""
if [ -n "$cwd" ]; then
    branch=$(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null)
    [ "$branch" = "HEAD" ] && branch=$(git -C "$cwd" rev-parse --short HEAD 2>/dev/null)
fi

# Colors matching oh-my-posh palette (orange for folder, dimmed for rest)
ORANGE='\033[38;2;240;118;35m'
BLUE='\033[38;2;75;149;233m'
GREEN='\033[38;2;122;186;62m'
YELLOW='\033[38;2;243;174;53m'
MAGENTA='\033[38;2;198;120;221m'
RESET='\033[0m'
BOLD='\033[1m'

parts=""
add() {
    if [ -n "$parts" ]; then parts="${parts}  "; fi
    parts="${parts}$1"
}

# Folder segment (orange, bold - matching oh-my-posh path segment)
if [ -n "$folder" ]; then
    parts="${BOLD}${ORANGE}${folder}${RESET}"
fi

# zdev sandbox marker: zdev sets ZDEV_SANDBOX (the container name) in every
# `zdev claude` session; unset means Claude runs directly on the host.
if [ -n "$ZDEV_SANDBOX" ]; then
    add "${MAGENTA}⬢ sandbox${RESET}"
fi

if [ -n "$model" ]; then
    add "${BLUE}${model}${RESET}"
fi

# Git: branch name only, no dirty/staged/untracked counters
if [ -n "$branch" ]; then
    add "${GREEN}${branch}${RESET}"
fi

if [ -n "$used" ]; then
    if [ "$used" -ge 80 ]; then
        add "${YELLOW}ctx:${used}%${RESET}"
    else
        add "ctx:${used}%"
    fi
fi

# Subscription plan: show the rolling usage windows, never a price.
# No subscription window in the payload (API-key billing): show the spend.
if [ -n "$limit_5h" ]; then
    if [ -n "$reset_at" ]; then
        reset_time=$(date -r "$reset_at" +%H:%M 2>/dev/null || date -d "@$reset_at" +%H:%M 2>/dev/null)
    else
        reset_time=""
    fi
    session_label="5h ${limit_5h}%"
    if [ -n "$reset_time" ]; then
        session_label="${session_label} ⏳${reset_time}"
    fi
    if [ -n "$limit_7d" ]; then
        session_label="${session_label} · 7d ${limit_7d}%"
    fi
    if [ "$limit_5h" -ge 80 ] || { [ -n "$limit_7d" ] && [ "$limit_7d" -ge 80 ]; }; then
        add "${YELLOW}${session_label}${RESET}"
    else
        add "$session_label"
    fi
elif [ -n "$cost" ]; then
    add "\$${cost}"
fi

printf '%b\n' "$parts"
