require "fileutils"

out_dir = "../out"

oldGoPath = ENV["GOPATH"]
ENV["GOPATH"] = Dir.pwd

at_exit {
	ENV["GOPATH"] = oldGoPath
}

def exec_or_die( cmd, current_dir = nil )
	orgDir = Dir.pwd
	Dir.chdir(current_dir) if current_dir != nil
	
	res = `#{cmd}`
	
	Dir.chdir(orgDir)

	if $?.exitstatus != 0
		print(res)
		exit(false)
	end
end

case ARGV[0]
	when "prepare"	then
		exec_or_die( "go install github.com/IMQS/router-core" )
		FileUtils.cp( "bin/router-core.exe", out_dir + '/bin/imqsrouter.exe' )
	when "test_unit" then
		# At present the tests behave no differently when run with -race and without,
		# but it's a likely thing to do in future. ie.. make some stress tests run only with -race off,
		# because -race uses 10x the memory and is 10x slower.
		exec_or_die( "go test github.com/IMQS/router-core/router -test.cpu 2" )
	when "test_integration" then
		# TODO: try logging into our IMQS domain (or whatever's appropriate for a CI box)
end

