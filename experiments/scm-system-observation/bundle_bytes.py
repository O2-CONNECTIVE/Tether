"""Exact UTF-8/LF artifact metadata, independent of the build host."""
def write_utf8_lf(path,text):
    path.write_bytes(text.encode('utf-8'))
