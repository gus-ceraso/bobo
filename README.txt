bobo, a simple language model harness

Usage: bobo [-hcfvwy] [-m model] [-e effort] [-l logfile]

Options:
  -h           Show this help message.
  -c           Enable explicit prompt caching.
  -w           Enable filesystem write.
  -y           YOLO mode: enable write and execution.
  -m model     Select model (ex. openai/gpt-6-luna).
  -e effort    Select effort (ex. xhigh).
  -l logfile   Verbose log to file.

TOOLS
read(path, offset, limit) match(regex) find(glob)
write(path, text, oldText) move(newpath, oldpath) copy(new, old) delete(new, old)
exec(binary, argv)

Models must have image support

Do not autoload AGENTS.md because of instruction hierarchy

Prompt:
You are a helpful assistant. Always answer in utf-8 plain text. You are in $CWD.
