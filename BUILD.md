# build instructions

first, download and install a recent version of [`oniguruma`](https://github.com/kkos/oniguruma) (dezip needs it for syntax highlighting):

```bash
curl -OL https://github.com/kkos/oniguruma/releases/download/v6.9.8/onig-6.9.8.tar.gz
tar xzf onig-6.9.8.tar.gz
cd onig-6.9.8
./configure && make && sudo make install
```

then you can build and run dezip itself:

```bash
curl -OL https://dezip.org/dezip-1.1.zip
unzip dezip-1.1.zip
cd dezip
go mod tidy
go build
./dezip
```

navigate to [http://localhost:8001](http://localhost:8001) to see it in action.

## notes

dezip writes to 3 subdirectories of the working directory:

* `root` is the web root,
* `meta` contains metadata about each archive,
* and `text` contains searchable renderings of markdown files.

If you want to run dezip from a different directory, make sure to copy or symlink `root/dezip.js` and `root/style.css` in order for javascript and css to work.

to enable syntax highlighting, set the `DEZIP_SYNTAX` environment variable to a directory full of [textmate](https://github.com/textmate/textmate) [language grammar](https://macromates.com/manual/en/language_grammars) files in `.plist` or `.tmLanguage` format. Here's the one I'm using: [https://dezip.org/syntax-2020-01-17.zip](https://dezip.org/syntax-2020-01-17.zip).

dezip.org routes http requests through nginx&mdash;rendered files are served directly from the filesystem, and other requests are forwarded to the dezip service itself.  here's a snippet of nginx config file which may be helpful if you're interested in doing that too:

```nginx
root /home/dezip/root;
index hidden.from.dezip.html;
location = / {
    return 302 $scheme://$server_name/v1/6/https/dezip.org/dezip-1.0.zip/dezip/README.md;
}
location / {
    try_files $uri =404;
}
location ~ ^/[a-zA-Z][a-zA-Z0-9+.-]*:/ {
    error_page 404 = @cachemiss;
}
location /v1/ {
    error_page 404 = @cachemiss;
    if ( $is_args = "?" ) { return 404; }
    types { }
    default_type "text/html;charset=utf-8";
    try_files $uri ${uri}hidden.from.dezip.html =404;
}
location @cachemiss {
    proxy_pass http://127.0.0.1:8001;
}
```
