# dezip

## What is this?

**dezip** is a website for browsing source code archives.

to use it, type `dezip.org/` in your address bar, then the address of the archive file, like this:

[`dezip.org/https://www.lua.org/ftp/lua-5.4.2.tar.gz`](https://dezip.org/https://www.lua.org/ftp/lua-5.4.2.tar.gz)

## What motivated you to make it?

discomfort with the centralization of software development into sites like github and gitlab. convenient source code browsing shouldn't be coupled so tightly to repository hosting services.

## Which protocols and archive formats are supported?

currently, the following protocols are supported: `ftp://`, `gemini://`, `gopher://`, `http://`, and `https://`; and the following archive formats, identified by file extension: `.zip`, `.tgz`, `.tar.gz`, `.tb2`, `.tbz`, `.tbz2`, `.tar.bz2`, `.txz`, and `.tar.xz`.

## Is there a way to search?

yeah!  click the **magnifying glass button** or press **f** to bring up the search field.  selected text will appear in the field automatically (so you don't have to copy and paste it).  press enter to search.  **j** and **k** move forward and backward through search results.

## Where can I find the source code?

the current version is available here: [dezip-1.0.zip](https://dezip.org/dezip-1.0.zip) [[browse](https://dezip.org/https://dezip.org/dezip-1.0.zip)]

see [BUILD.md](BUILD.md) for build instructions.

## Who made this?

dezip was made by **ian henderson**. feel free to email me at [ian@ianhenderson.org](mailto:ian@ianhenderson.org) or contact me on twitter at [@ianh_](https://twitter.com/ianh_).

Syntax hightlighting for rendered Markdown content added by Petra Mirelli (@iNtEgraIR2021).

## TODO

* add dark mode -> improve styling: more contrast and less css filter
* minify content and assets (using [`github.com/tdewolff/minify`](https://github.com/tdewolff/minify)(?))
* use `chroma` for syntax highlighting -> fix font size, line numbers
