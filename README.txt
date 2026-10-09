bobo, a simple language model harness

Usage: bobo [-hwy] [-m model] [-e effort]

Options:
  -h          Show this help message.
  -c          Enable explicit prompt caching.
  -w          Enable filesystem write.
  -s          Enable subagents.
  -y          YOLO mode: enable write, bash, and subagents.
  -m model    Select model (ex. openai/gpt-6-luna)
  -e effort   Select effort (ex. xhigh)

read_text(resource string, offset int64)
read_pdf
read_image
fetch_text
fetch_pdf
fetch_image
write_text
write_image
edit_text
edit_image
search_web
search_file
search_content
exec
