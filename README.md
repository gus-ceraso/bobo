# bobo

A simple and portable harness for language models

## Introduction

### The agent loop

## Installation

bobo runs on every operating system and architecture [supported by the Go 1.27
runtime](https://go.dev/wiki/MinimumRequirements). If it doesn't run properly on
your system, [let me know](mailto:gus@ceraso.dev).

```
$ git clone https://ceraso.dev/bobo.git && cd bobo && make && sudo make install
```

## Usage

```
$ bobo -h
Usage: bobo [-hcfvwy] [-m model] [-e effort] [-l logfile]

Options:
  -h           Show this help message.
  -c           Enable explicit prompt caching.
  -f           Disable web search and fetch.
  -w           Enable filesystem write.
  -y           YOLO mode: enable write and execution.
  -m model     Select model (ex. openai/gpt-6-luna).
  -e effort    Select effort (ex. xhigh).
  -l logfile   Verbose log to file.
```

### Patterns

#### Implementation

#### Documentation

#### Review

#### Research

#### Tests

#### Experiments

#### Automation

#### Agentic "Engineering"

### Choice of language model

## Tools

### Read

#### Read

```
/*
Read a local text file, PDF, image (JPEG, PNG, or WebP), or directory.
Text files return lines, PDFs return pages as images, and directories
return entry names, with a trailing "/" on subdirectories. The result
includes the total line, page, or entry count for paging. Text files
with lines longer than 64 KiB aren't supported.
*/
declare function read(
	/*
	Path to the file or directory. Must be a relative path inside the
	working directory. Hidden files and directories (names starting with
	".") aren't allowed.
	*/
	path: string,
	/*
	1-based line, PDF page, or directory entry to start from. Defaults to 1.
	Ignored for images.
	*/
	offset?: number,
	/*
	Maximum lines, PDF pages, or directory entries to return. Defaults to
	1000 lines or entries, or 5 pages. Ignored for images.
	*/
	limit?: number,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The path argument.
		*/
		path?: string,
		/*
		Detected media type, such as "text/plain; charset=utf-8", or
		"inode/directory" for directories.
		*/
		media_type?: string,
		/*
		The offset argument, or its default.
		*/
		offset?: number,
		/*
		The limit argument, or its default.
		*/
		limit?: number,
		/*
		Total lines, PDF pages, or directory entries.
		*/
		total?: number,
	},
	/*
	Text lines or directory entries, one per line; the image; or one image
	per PDF page.
	*/
	...(string | Uint8Array)[]
];
```

#### Find

```
/*
Recursively find files whose paths match a regular expression. Returns
one path per line, relative to the search directory, in lexical order.
Directories aren't listed, and symlinks are listed but not followed. The
result includes the total match count for paging.
*/
declare function find(
	/*
	RE2 regular expression matched against each relative path, such as
	"\.go$". It matches anywhere in the path unless anchored with ^ or $.
	Matching is case-sensitive; use (?i) to ignore case.
	*/
	pattern: string,
	/*
	Directory to search. Defaults to ".". Must be a relative path inside the
	working directory. Hidden files and directories (names starting with
	".") aren't allowed.
	*/
	path?: string,
	/*
	1-based match to start from. Defaults to 1.
	*/
	offset?: number,
	/*
	Maximum matches to return. Defaults to 1000.
	*/
	limit?: number,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The pattern argument.
		*/
		pattern?: string,
		/*
		The path argument, or its default.
		*/
		path?: string,
		/*
		The offset argument, or its default.
		*/
		offset?: number,
		/*
		The limit argument, or its default.
		*/
		limit?: number,
		/*
		Total matches.
		*/
		total?: number,
	},
	/*
	Matching paths, one per line.
	*/
	...string[]
];
```

#### Grep

```
/*
Recursively search text files for lines matching a regular expression.
Returns one match per line as path:line_number:line, with paths relative
to the search directory, in lexical order. Symlinks, files that aren't
text, and files with lines longer than 64 KiB are skipped. The result
includes the total match count for paging.
*/
declare function grep(
	/*
	RE2 regular expression matched against each line. It matches anywhere in
	the line unless anchored with ^ or $. Matching is case-sensitive; use
	(?i) to ignore case.
	*/
	pattern: string,
	/*
	Directory to search. Defaults to ".". Must be a relative path inside the
	working directory. Hidden files and directories (names starting with
	".") aren't allowed.
	*/
	path?: string,
	/*
	RE2 regular expression that limits the search to files whose relative
	paths match, such as "\.go$". Matches anywhere in the path unless
	anchored. Defaults to all files.
	*/
	path_pattern?: string,
	/*
	1-based match to start from. Defaults to 1.
	*/
	offset?: number,
	/*
	Maximum matches to return. Defaults to 1000.
	*/
	limit?: number,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The pattern argument.
		*/
		pattern?: string,
		/*
		The path argument, or its default.
		*/
		path?: string,
		/*
		The path_pattern argument, or its default.
		*/
		path_pattern?: string,
		/*
		The offset argument, or its default.
		*/
		offset?: number,
		/*
		The limit argument, or its default.
		*/
		limit?: number,
		/*
		Total matches.
		*/
		total?: number,
	},
	/*
	Matches, one per line, as path:line_number:line.
	*/
	...string[]
];
```

#### Search

```
/*
Search the web with Exa. Returns up to 10 results as a JSON array of
objects with title, url, and highlights, which are excerpts relevant to
the query. Use fetch to read a full page.
*/
declare function search(
	/*
	Natural-language description of what to find, such as "Go 1.24 release
	notes". Include specific names, versions, or dates when relevant.
	*/
	query: string,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The query argument.
		*/
		query?: string,
		/*
		Number of results.
		*/
		total?: number,
	},
	/*
	Results. Absent if there are none.
	*/
	{
		/*
		Page title.
		*/
		title: string,
		/*
		Page URL.
		*/
		url: string,
		/*
		Excerpts relevant to the query.
		*/
		highlights: string[],
	}[]?,
];
```

#### Fetch

```
/*
Fetch a URL with HTTP GET. HTML pages return their extracted text, other
text and JSON return lines, PDFs return pages as images, and JPEG, PNG,
and WebP images are returned as is. The result includes the total line
or page count for paging. Responses are cached for 30 minutes. Text with
lines longer than 64 KiB isn't supported.
*/
declare function fetch(
	/*
	HTTP or HTTPS URL to fetch.
	*/
	url: string,
	/*
	1-based line or PDF page to start from. Defaults to 1. Ignored for
	images.
	*/
	offset?: number,
	/*
	Maximum lines or PDF pages to return. Defaults to 1000 lines or 5 pages.
	Ignored for images.
	*/
	limit?: number,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The url argument.
		*/
		url?: string,
		/*
		Content-Type of the response, such as "text/html; charset=utf-8".
		*/
		media_type?: string,
		/*
		The offset argument, or its default.
		*/
		offset?: number,
		/*
		The limit argument, or its default.
		*/
		limit?: number,
		/*
		Total lines or PDF pages.
		*/
		total?: number,
	},
	/*
	Text lines, one per line; the image; or one image per PDF page.
	*/
	...(string | Uint8Array)[]
];
```

### Write

#### Write

```
/*
Create a file or replace an existing file's contents with text. Missing
parent directories are created. Prefer edit for small changes to
existing files.
*/
declare function write(
	/*
	Path to the file. Must be a relative path inside the working directory.
	Hidden files and directories (names starting with ".") aren't allowed.
	*/
	path: string,
	/*
	Complete new contents of the file.
	*/
	text: string,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The path argument.
		*/
		path?: string,
	},
];
```

#### Edit

```
/*
Replace exactly one occurrence of old_text with new_text in an existing
text file. Matching is literal and case-sensitive, including whitespace
and line breaks. The edit fails if old_text isn't found or occurs more
than once. Include surrounding lines to make it unique.
*/
declare function edit(
	/*
	Path to the file. Must be a relative path inside the working directory.
	Hidden files and directories (names starting with ".") aren't allowed.
	*/
	path: string,
	/*
	Exact text to replace. Must occur exactly once in the file.
	*/
	old_text: string,
	/*
	Replacement text. Use an empty string to delete old_text.
	*/
	new_text: string,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The path argument.
		*/
		path?: string,
	},
];
```

#### Copy

```
/*
Copy a file, directory, or symlink. Missing parent directories of
new_path are created. Directories are copied recursively and merged into
an existing new_path. Existing files are overwritten. Symlinks are
copied as links, not followed, and file permissions are preserved.
new_path is the copy itself, not a directory to copy into, and it can't
be old_path or inside it. Copying stops at the first error, which can
leave a partial copy.
*/
declare function copy(
	/*
	Path to copy. Must be a relative path inside the working directory.
	Hidden files and directories (names starting with ".") aren't allowed.
	*/
	old_path: string,
	/*
	Path of the copy. Must be a relative path inside the working directory.
	Hidden files and directories (names starting with ".") aren't allowed.
	*/
	new_path: string,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The old_path argument.
		*/
		old_path?: string,
		/*
		The new_path argument.
		*/
		new_path?: string,
	},
];
```

#### Move

```
/*
Move or rename a file, directory, or symlink. new_path is the new name,
not a directory to move into. Missing parent directories of new_path are
created. An existing file at new_path is overwritten.
*/
declare function move(
	/*
	Path to move. Must be a relative path inside the working directory.
	Hidden files and directories (names starting with ".") aren't allowed.
	*/
	old_path: string,
	/*
	New path. Must be a relative path inside the working directory. Hidden
	files and directories (names starting with ".") aren't allowed.
	*/
	new_path: string,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The old_path argument.
		*/
		old_path?: string,
		/*
		The new_path argument.
		*/
		new_path?: string,
	},
];
```

#### Remove

```
/*
Permanently delete a file, symlink, or directory with all of its
contents. Symlinks are deleted, not followed. Succeeds if path doesn't
exist.
*/
declare function remove(
	/*
	Path to delete. Can't be the working directory. Must be a relative path
	inside the working directory. Hidden files and directories (names
	starting with ".") aren't allowed.
	*/
	path: string,
): [
	{
		/*
		Error message. Absent on success.
		*/
		error?: string,
		/*
		The path argument.
		*/
		path?: string,
	},
];
```

### YOLO

#### Exec

```
/*
Run a program in the working directory and return the last lines of its
combined stdout and stderr, along with its exit code. The program runs
directly, without a shell, so pipes, redirects, globs, and variables
aren't expanded. To use them, run "sh" with the arguments "-c" and the
command. Stdin is empty. Output that isn't text or has lines longer than
64 KiB isn't supported.
*/
declare function exec(
	/*
	Program to run. A name without a "/" is looked up in PATH.
	*/
	path: string,
	/*
	Arguments passed to the program as is, one per element, without shell
	quoting.
	*/
	arguments?: string[],
	/*
	Seconds to wait before the program is killed. Defaults to 300.
	*/
	timeout?: number,
	/*
	Maximum number of output lines to return, counted from the end. Defaults
	to 1000.
	*/
	limit?: number,
): [
	{
		/*
		Error message, such as "exit status 1". Absent on success.
		*/
		error?: string,
		/*
		The path argument.
		*/
		path?: string,
		/*
		The arguments argument.
		*/
		arguments?: string[],
		/*
		The timeout argument, or its default.
		*/
		timeout?: number,
		/*
		The limit argument, or its default.
		*/
		limit?: number,
		/*
		Exit code, or -1 if the program was killed by a signal. Absent if the
		program didn't start.
		*/
		exit_code?: number,
	},
	/*
	Last lines of combined stdout and stderr.
	*/
	...string[]
];
```

## Rationale

Models must have image support

Do not autoload AGENTS.md because of instruction hierarchy

Without -y, the model is restricted to the current working directory and can only read/write files outside cwd if they are symlinks.

Prompt:
You are a helpful assistant. Always answer the user in utf-8 plain text.
