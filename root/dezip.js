// -- search

var searchIsOpen = false;
var searchField = document.getElementById("search-field");
var originalSearch = searchField.value;
function openSearch(event) {
    searchIsOpen = true;
    var blur = document.createElement("div");
    blur.id = "blur";
    blur.onclick = closeSearch;
    document.body.insertBefore(blur, document.body.firstChild);
    document.getElementById("search-header").style.visibility = "visible";
    document.getElementById("path-header").style.visibility = "hidden";
    searchField.value = window.getSelection().toString();
    searchField.focus();
    event.preventDefault();
    return false;
}
function closeSearch(event) {
    searchIsOpen = false;
    var blur = document.getElementById("blur");
    blur.parentNode.removeChild(blur);
    searchField.blur();
    document.getElementById("search-header").style.visibility = "hidden";
    document.getElementById("path-header").style.visibility = "visible";
    event.preventDefault();
    return false;
}
var searchIsTogglable = document.getElementById("open-search") !== null;
if (document.getElementById("open-search") !== null) {
    document.getElementById("open-search").title = "key shortcut: f";
    document.getElementById("open-search").onclick = openSearch;
}
if (document.getElementById("cancel-search") !== null)
    document.getElementById("cancel-search").onclick = closeSearch;
if (document.getElementById("submit-search") !== null) {
    document.getElementById("submit-search").onclick = function (event) {
        document.getElementById("search-form").submit();
        event.preventDefault();
        return false;
    };
}
var filterText = "";
if (document.getElementById("search-filter") !== null) {
    filterText = document.getElementById("search-filter").textContent;
    if (filterText.slice(0, 2) == " [") {
        filterText = filterText.slice(2, -1);
        document.getElementById("search-filter").innerHTML = " [<a href='javascript:filter()'>" + filterText.replace(/&/g, "&amp;").replace(/</g, "&lt;") + "</a>]";
    } else {
        filterText = "";
        document.getElementById("search-filter").innerHTML = " [<a href='javascript:filter()'>filter...</a>]";
    }
}
function filter() {
    var entered = prompt("search in paths matching regexp (empty to search all files):", filterText);
    if (entered === null)
        return;
    if (entered === filterText)
        return;
    filterText = entered;
    document.cookie = "filter=" + encodeURIComponent(filterText);
    location.search = "?search=" + encodeURIComponent(originalSearch);
}
var maximumFocus = 1;
var searchResults = [];
window.addEventListener("load", function (event) {
    searchResults = document.getElementsByClassName("search-result");
    if (searchResults.length > 0) {
        for (var i = 0; i < searchResults.length; ++i) {
            if (searchResults[i].id == window.location.hash.slice(1))
                searchResults[i].focus();
            var n = parseInt(searchResults[i].id, 10);
            if (n > maximumFocus)
                maximumFocus = n;
        }
    }
});
window.addEventListener("keydown", function (event) {
    if (event.shiftKey || event.ctrlKey || event.altKey || event.metaKey)
        return true;
    if (event.keyCode == 27 && searchIsTogglable && searchIsOpen)
        return closeSearch(event);
    else if (event.keyCode == 70 && searchIsTogglable && !searchIsOpen)
        return openSearch(event);
    else if (event.keyCode == 27 && !searchIsTogglable) {
        searchField.blur();
        searchField.value = originalSearch;
        event.preventDefault();
        return false;
    } else if (event.keyCode == 70 && !searchIsTogglable && document.activeElement !== searchField) {
        var selection = window.getSelection().toString();
        if (selection !== "")
            searchField.value = selection;
        searchField.focus();
        if (searchField.value !== "" && selection === "")
            searchField.select();
        event.preventDefault();
        return false;
    } else if ((event.keyCode == 74 || event.keyCode == 75) && searchResults.length > 0 && (!document.activeElement || document.activeElement.tagName.toLowerCase() !== "input")) {
        var currentFocus = parseInt(document.activeElement.id, 10);
        if (!(currentFocus == currentFocus)) {
            var i = 0;
            for (; i < searchResults.length; ++i) {
                if (searchResults[i].getBoundingClientRect().top > 0)
                    break;
            }
            if (i < searchResults.length)
                currentFocus = i;
            else
                currentFocus = 0;
        }
        if (event.keyCode == 74) {
            currentFocus++;
            if (currentFocus > maximumFocus)
                currentFocus = 1;
        } else if (event.keyCode == 75) {
            currentFocus--;
            if (currentFocus < 1)
                currentFocus = maximumFocus;
        }
        document.getElementById(currentFocus).focus();
        event.preventDefault();
        return false;
    }
});
window.addEventListener("pageshow", function (event) {
    if (searchIsOpen)
        closeSearch(event);
});

// -- progress bar

if (document.getElementById("progress-overlay") !== null) {
    var needsReload = false;
    // to prevent a sudden page transition, show the progress bar for at least a
    // minimum amount of time.
    var minimumProgressTimeout = window.setTimeout(function () {
        if (needsReload)
            location.reload();
        minimumProgressTimeout = null;
    }, 500);
    // grab progress updates via xhr.
    var interval = window.setInterval(function () {
        var xhr = new XMLHttpRequest();
        xhr.open("GET", location.href);
        xhr.responseType = "document";
        xhr.timeout = 200;
        xhr.setRequestHeader("X-Dezip-Progress", "1");
        xhr.onload = function (e) {
            console.log(xhr);
            if (xhr.readyState !== 4 || xhr.status !== 200 && xhr === null)
                location.reload();
            else if (xhr.responseXML.getElementById("progress-overlay") === null) {
                window.clearInterval(interval);
                var pathHeader = document.getElementById("path-header");
                pathHeader.parentNode.replaceChild(xhr.responseXML.getElementById("path-header"), pathHeader);
                if (minimumProgressTimeout !== null)
                    needsReload = true;
                else
                    location.reload();
            } else if (document.getElementById("progress-overlay") !== null) {
                document.getElementById("progress-overlay").style.left =
                 xhr.responseXML.getElementById("progress-overlay").style.left;
            }
        };
        xhr.send(null);
    }, 200);
}

// -- mobile fixes

if (navigator.userAgent.match(/Mobi/i)) {
    // this feels more natural on mobile platforms, but awkward on desktops.
    searchField.onblur = closeSearch;
}

if (navigator.userAgent.match(/\bAppleWebKit\b/) &&
 navigator.userAgent.match(/\bMobile\b/)) {
    // the "touch-action: manipulation" css property disables double-tap-to-zoom
    // in order to make the tap gesture recognizer recognize faster.  but for
    // some reason it only works if there's an onclick handler.
    window.addEventListener("load", function (event) {
        var links = document.getElementsByTagName("a");
        for (var i = 0; i < links.length; ++i) {
            if (links[i].onclick === null)
                links[i].onclick = function () {};
        }
    });
}
