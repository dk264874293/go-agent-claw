import json,pathlib,sys
work=pathlib.Path(sys.argv[1])
actual=json.loads((work/'agentops.config.json').read_text())
expected=json.loads(pathlib.Path(__file__).with_name('expected.json').read_text())
assert actual==expected,'目标字段错误或修改了其他字段'
print('授权成员字段与非目标字段核对通过')
