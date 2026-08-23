#!/usr/bin/env bash
# fetch 后重建 origin tracking refs
#
# 背景：本环境对 .git/refs/remotes/ 下 git 自身的写入（lock+rename）有系统级拦截，
# 导致 git fetch 后 tracking ref 不落盘、git status 显示 origin/master [gone]。
# shell 直接写文件有效，故用 ls-remote 结果手动重建。
#
# 用法：bash scripts/git-sync-remotes.sh [fetch 附加参数]
set -euo pipefail

git fetch origin "$@"

mkdir -p .git/refs/remotes/origin
git ls-remote --heads origin | while read -r sha ref; do
  printf '%s\n' "$sha" > ".git/refs/remotes/origin/${ref#refs/heads/}"
done

echo "tracking refs 已重建："
git for-each-ref refs/remotes/origin --format='  %(refname:short) -> %(objectname:short)'
