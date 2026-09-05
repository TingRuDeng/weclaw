import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// App's pipe changes when it restarts. Resolve its current, protected value
// whenever the official MCP starts, without changing peer authorization.
const file = path.join(path.dirname(fileURLToPath(import.meta.url)), 'app-pipe.json');
const info = fs.lstatSync(file);
if (!info.isFile() || info.isSymbolicLink() || (info.mode & 0o077) || info.uid !== process.getuid()) {
  throw new Error('Invalid WeClaw App pipe configuration');
}
const pipe = JSON.parse(fs.readFileSync(file, 'utf8'));
if (typeof pipe !== 'string' || !path.isAbsolute(pipe)) throw new Error('Invalid Codex App pipe path');
process.env.CODEX_APP_TOOLS_PIPE_PATH = pipe;
