import unittest
from unittest.mock import patch
import json
import subprocess
import gitee_retention as retention


def release(version, names=None):
    return {'id': version, 'tag_name': f'v0.1.{version}', 'attachments': [
        {'id': version * 10 + i, 'name': n, 'size': 10}
        for i, n in enumerate(names or sorted(retention.REQUIRED))]}


class RetentionTests(unittest.TestCase):
    def test_numeric_order_and_three_complete_versions(self):
        kept, deleted = retention.plan([release(i) for i in (8, 9, 10, 11)], 'v0.1.11')
        self.assertEqual(kept, {'v0.1.9', 'v0.1.10', 'v0.1.11'})
        self.assertEqual({r['tag_name'] for r, a in deleted}, {'v0.1.8'})

    def test_failed_upload_does_not_displace_available_versions(self):
        rows = [release(i) for i in (8, 9, 10)] + [release(11, ['checksums.txt'])]
        kept, deleted = retention.plan(rows, 'v0.1.11')
        self.assertEqual(len(kept), 4)
        self.assertEqual(deleted, [])

    def test_unknown_assets_and_prereleases_are_not_deleted(self):
        rows = [release(i) for i in (8, 9, 10, 11)]
        rows[0]['attachments'].append({'id': 99, 'name': 'notes.pdf', 'size': 10})
        rows.append(dict(release(7), tag_name='v0.1.7-rc1'))
        _, deleted = retention.plan(rows, 'v0.1.11')
        self.assertEqual({a['name'] for r, a in deleted}, retention.REQUIRED)

    def test_duplicate_assets_fail_closed(self):
        row = release(8)
        row['attachments'].append(row['attachments'][0])
        with self.assertRaises(ValueError):
            retention.plan([row], 'v0.1.8')


class BoundaryTests(unittest.TestCase):
    def test_pagination_reads_all_pages(self):
        api = retention.API('/unused')
        batch = [{'id': i} for i in range(1, 101)]
        with patch.object(api, 'request', side_effect=[batch, [{'id': 101}]]):
            self.assertEqual(len(api.listing('/releases')), 101)

    def test_repeated_page_fails_closed(self):
        api = retention.API('/unused')
        batch = [{'id': i} for i in range(1, 101)]
        with patch.object(api, 'request', side_effect=[batch, batch]):
            with self.assertRaises(ValueError):
                api.listing('/releases')

    def test_missing_backup_blocks_deletion(self):
        result = subprocess.CompletedProcess([], 0, stdout='[[]]')
        with patch.object(retention.subprocess, 'run', return_value=result):
            with self.assertRaisesRegex(ValueError, '备份缺失'):
                retention.backup_check([(release(8), release(8)['attachments'][0])])

    def test_backup_requires_original_binary_and_checksums(self):
        row = release(8)
        assets = [{'name': n.removesuffix('.gz'), 'state': 'uploaded', 'size': 10}
                  for n in retention.REQUIRED]
        result = subprocess.CompletedProcess([], 0, stdout=json.dumps([[{
            'tag_name': row['tag_name'], 'draft': False, 'prerelease': False, 'assets': assets}]]))
        with patch.object(retention.subprocess, 'run', return_value=result):
            retention.backup_check([(row, a) for a in row['attachments']])

    def test_http_error_is_not_an_empty_inventory(self):
        api = retention.API('/unused')
        result = subprocess.CompletedProcess([], 0, stdout='[]\n403')
        with patch.object(retention.subprocess, 'run', return_value=result):
            with self.assertRaises(RuntimeError):
                api.listing('/releases')


if __name__ == '__main__':
    unittest.main()
