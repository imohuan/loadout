import zipfile
path = 'C:/Users/Administrator/Desktop/loadout-config-20261015-072311.zip'
try:
    z = zipfile.ZipFile(path)
    names = z.namelist()
    print('ok, entries:', len(names))
    for n in names[:20]:
        print(' ', n)
    bad = z.testzip()
    print('first bad entry:', bad)
except Exception as e:
    print('FAILED:', type(e).__name__, e)
