content = open("pkg/mesh/api/bench_test.go").read()
new_content = []
for line in content.splitlines():
    new_content.append(line)
    if ':= newTestNode(' in line and 'defer' not in line:
        var_name = line.strip().split(':=')[0].strip()
        indent = line[:len(line) - len(line.lstrip())]
        new_content.append(f"{indent}defer func() {{ _ = {var_name}.Close() }}()")

open("pkg/mesh/api/bench_test.go", "w").write('\n'.join(new_content))
