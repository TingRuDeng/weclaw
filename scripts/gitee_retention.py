#!/usr/bin/env python3
"""仅清理有 GitHub 权威备份的旧安装附件；默认只预览。"""
import argparse
import json
import os
import re
import subprocess
import sys

REQUIRED = {'checksums.txt', 'weclaw_darwin_arm64.gz', 'weclaw_linux_amd64.gz'}
KNOWN = REQUIRED | {'weclaw_darwin_amd64.gz', 'weclaw_linux_arm64.gz'}


def version(tag):
    match = re.fullmatch(r'v(\d+)\.(\d+)\.(\d+)', tag)
    return tuple(map(int, match.groups())) if match else None


def plan(releases, target):
    stable = [r for r in releases if version(r['tag_name']) and not r.get('prerelease')]
    tags = [r['tag_name'] for r in stable]
    if len(tags) != len(set(tags)):
        raise ValueError('重复版本，停止清理')
    for row in stable:
        assets = row['attachments']
        names = [a['name'] for a in assets]
        ids = [a['id'] for a in assets]
        if len(names) != len(set(names)) or len(ids) != len(set(ids)):
            raise ValueError('重复附件，停止清理')
        if any(type(a['id']) is not int or a['id'] <= 0 or
               type(a['size']) is not int or a['size'] <= 0 for a in assets):
            raise ValueError('附件元数据无效，停止清理')
    complete = [r for r in stable if REQUIRED <= {a['name'] for a in r['attachments']}]
    complete.sort(key=lambda r: version(r['tag_name']), reverse=True)
    keep = {r['tag_name'] for r in complete[:3]} | {target}
    # 不完整的新版本不能占用三个可用版本的恢复名额。
    deleted = [(r, a) for r in stable if r['tag_name'] not in keep
               for a in r['attachments'] if a['name'] in KNOWN]
    return keep, deleted


class API:
    def __init__(self, header):
        self.header = header
        self.base = os.environ.get('GITEE_API_BASE', 'https://gitee.com/api/v5').rstrip('/')
        self.repo = os.environ.get('GITEE_REPO', 'jimdeng891/weclaw')

    def request(self, path, method='GET'):
        result = subprocess.run([
            'curl', '-sS', '--proto', '=https', '--tlsv1.2', '--connect-timeout', '30',
            '--max-time', '120', '--header', '@' + self.header, '-X', method,
            '-w', '\n%{http_code}', self.base + '/repos/' + self.repo + path,
        ], capture_output=True, text=True)
        body, _, status = result.stdout.rpartition('\n')
        expected = '204' if method == 'DELETE' else '200'
        if result.returncode or status != expected:
            # 不打印可能包含凭据的响应正文。
            raise RuntimeError(f'Gitee {method} {path}: HTTP {status}, curl={result.returncode}')
        return json.loads(body) if body else None

    def listing(self, path):
        rows, seen, page = [], set(), 1
        while True:
            batch = self.request(f'{path}?per_page=100&page={page}')
            if not isinstance(batch, list):
                raise ValueError('列表响应无效')
            for item in batch:
                key = item['id']
                if type(key) is not int or key <= 0 or key in seen:
                    raise ValueError('分页重复或 ID 无效，停止清理')
                seen.add(key)
                rows.append(item)
            if len(batch) < 100:
                return rows
            page += 1

    def inventory(self):
        rows = self.listing('/releases')
        for row in rows:
            row['attachments'] = self.listing(f"/releases/{row['id']}/attach_files")
        return rows


def backup_check(deleted):
    # 一次分页读取所有 GitHub Release；只有公开且已上传完成的对应资产才算恢复来源。
    result = subprocess.run([
        'gh', 'api', '--paginate', '--slurp',
        'repos/TingRuDeng/weclaw/releases?per_page=100',
    ], check=True, capture_output=True, text=True)
    backups = {r['tag_name']: r for page in json.loads(result.stdout) for r in page
               if not r['draft'] and not r['prerelease']}
    for row, asset in deleted:
        backup = backups.get(row['tag_name'], {})
        names = {a['name'] for a in backup.get('assets', [])
                 if a.get('state') == 'uploaded' and a.get('size', 0) > 0}
        source = asset['name'].removesuffix('.gz')
        if not {source, 'checksums.txt'} <= names:
            raise ValueError(f"GitHub 备份缺失：{row['tag_name']} {source}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('tag')
    parser.add_argument('--auth-header', required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    if not version(args.tag):
        parser.error('tag 必须是 vX.Y.Z')
    api = API(args.auth_header)
    if api.request('').get('full_name') != api.repo:
        raise ValueError('目标仓库不匹配')
    rows = api.inventory()
    keep, deleted = plan(rows, args.tag)
    backup_check(deleted)
    print('保护版本：' + ', '.join(sorted(keep, key=version)), flush=True)
    print(f'清理预览：{len(deleted)} 个附件，{sum(a["size"] for r, a in deleted) / 1048576:.2f} MiB', flush=True)
    for row, asset in deleted:
        print(f"  {row['tag_name']} / {asset['name']} ({asset['size']} bytes)", flush=True)
    if not args.apply:
        return
    # 开始删除前核对整个清单未变化；所有 API/备份检查都先于第一次删除。
    if api.inventory() != rows:
        raise ValueError('盘点期间附件发生变化，停止清理')
    for row, asset in deleted:
        path = f"/releases/{row['id']}/attach_files/{asset['id']}"
        current = api.request(path)
        if any(current.get(k) != asset[k] for k in ('id', 'name', 'size')):
            raise ValueError('删除前附件身份变化，停止清理')
        api.request(path, 'DELETE')
    remaining = api.inventory()
    _, pending = plan(remaining, args.tag)
    if pending:
        raise ValueError('清理后仍有待淘汰附件')
    print(f'清理完成：已删除 {len(deleted)} 个安装附件，Release 和源码标签保留。', flush=True)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, RuntimeError, KeyError, subprocess.CalledProcessError) as exc:
        print(f'Gitee 保留策略失败：{exc}', file=sys.stderr)
        sys.exit(1)
