import re
content = open("pkg/mesh/api/bench_test.go").read()

found = []
# Very simple parsing
lines = content.splitlines()
in_bench = False
bench_name = ""
in_loop = False

for line in lines:
    if line.startswith("func Benchmark"):
        in_bench = True
        bench_name = line
        in_loop = False
    elif line.startswith("}"):
        in_bench = False
    elif in_bench:
        if "for i := 0; i < b.N" in line:
            in_loop = True
        elif in_loop:
            if "defer " in line:
                found.append(f"{bench_name} -> defer in loop")
            elif "Listen(" in line or "ListenAddr(" in line or "AddPeer(" in line or "Dial(" in line:
                if "BenchmarkListen" not in bench_name:
                    found.append(f"{bench_name} -> creates network in loop without defer: {line}")
            elif re.search(r"go \w", line):
                found.append(f"{bench_name} -> spawns goroutine inside loop")

open("script_out.txt", "w").write("\n".join(found))
