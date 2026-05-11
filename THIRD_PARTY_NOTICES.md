# Third-Party Notices

wx-mcp source code does not commit third-party binary libraries.

Release zips may bundle `libWCDB.dylib` so the MCP server can load Tencent WCDB at runtime. WCDB is an upstream Tencent project; see its repository and license:

- https://github.com/Tencent/wcdb
- https://github.com/Tencent/wcdb/blob/master/LICENSE

`libWCDB.dylib` is loaded locally by wx-mcp for read-only access to the user's own WeChat databases.
