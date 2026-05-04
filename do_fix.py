import re

content = open("pkg/mesh/api/bench_test.go").read()

def repl(match):
    indent = match.group(1)
    var = match.group(2)
    expr = match.group(3)
    return f"{indent}{var} := newTestNode({expr})\n{indent}defer func() {{ _ = {var}.Close() }}()"

# Only replace where not already deferred and not inside the loop
new_content = re.sub(r'^(\s+)(\w+)\s*:=\s*newTestNode\((.*?)\)(?!\s*\n\s*defer)', repl, content, flags=re.MULTILINE)

open("pkg/mesh/api/bench_test.go", "w").write(new_content)
