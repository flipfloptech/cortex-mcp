import re
content = open("pkg/mesh/api/bench_test.go").read()

def repl(match):
    indent = match.group(1)
    var = match.group(2)
    expr = match.group(3)
    return f"{indent}{var} := newTestNode({expr})\n{indent}_ = {var}.Close() // manual cleanup via defer later\n{indent}defer func() {{ _ = {var}.Close() }}()"

new_content = []
for line in content.split('\n'):
    m = re.match(r'^(\s+)(\w+)\s*:=\s*newTestNode\((.*?)\)$', line)
    if m:
        if "BenchmarkNewTestNode" not in new_content[-3:]:
             indent = m.group(1)
             var = m.group(2)
             new_content.append(line)
             new_content.append(f"{indent}defer func() {{ _ = {var}.Close() }}()")
             continue
    new_content.append(line)

open("pkg/mesh/api/bench_test.go", "w").write('\n'.join(new_content))
