# Third-Party Notices

wechat-cli source code does not commit third-party binary libraries.

Release zips may bundle `libWCDB.dylib` / `libWCDB.dll` so the CLI can load Tencent WCDB at runtime. WCDB is an upstream Tencent project; see its repository and license:

- https://github.com/Tencent/wcdb
- https://github.com/Tencent/wcdb/blob/master/LICENSE

`libWCDB.dylib` / `libWCDB.dll` is loaded locally by wechat-cli for read-only access to the user's own WeChat databases.
