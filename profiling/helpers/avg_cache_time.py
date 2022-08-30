""" Generate statistics for different caching scenarios, using the output of `cache_time.sh` """
import re
import sys
import os.path

def parse_file(file_name, results):
    """
    Parsing a file generated by running `cache_time.sh`

    Parameters:
    filename(string): The name of the file to be parsed
    results(dict): The dictionary where the parsed data should be saved
    """
    file_channel = open(file_name, 'r', encoding='utf-8')
    lines = file_channel.readlines()

    for line in lines:
        field_1 = line.split("\t")
        field_2 = re.split("m|s", field_1[1])
        results[field_1[0]].append(float(field_2[0]) * 60 + float(field_2[1]))

if __name__ == "__main__":
    results = {"cold" : {"real" : [], "user" : [], "sys" : []},
            "warm" : {"real" : [], "user" : [], "sys" : []},
            "hot" : {"real" : [], "user" : [], "sys" : []}}
    results_avg = {"cold" : {"real" : 0, "user" : 0, "sys" : 0},
            "warm" : {"real" : 0, "user" : 0, "sys" : 0},
            "hot" : {"real" : 0, "user" : 0, "sys" : 0}}
    ratios_avg = {"cold / hot" : {"real" : 0, "user" : 0, "sys" : 0},
            "cold / warm" : {"real" : 0, "user" : 0, "sys" : 0},
            "warm / hot" : {"real" : 0, "user" : 0, "sys" : 0}}
    best_ratio_estimator_1 = {"cold" : [0, 0], "warm" : [0, 0], "hot" : [0, 0]}
    best_ratio_estimator_2 = {"cold / hot" : {"real" : [0, 0], "user" : [0, 0], "sys" : [0, 0]},
            "cold / warm" : {"real" : [0, 0], "user" : [0, 0], "sys" : [0, 0]},
            "warm / hot" : {"real" : [0, 0], "user" : [0, 0], "sys" : [0, 0]}}

    rounds = int(sys.argv[1])
    tmp_dir = str(sys.argv[2])


    parse_file(tmp_dir + "/cold_results", results["cold"])
    parse_file(tmp_dir + "/warm_results", results["warm"])
    parse_file(tmp_dir + "/hot_results", results["hot"])

    # check if we should also report results for a local benchmark
    if os.path.exists(tmp_dir + "/local_results"):
        results["local"] = {"real" : [], "user" : [], "sys" : []}
        results_avg["local"] = {"real" : 0, "user" : 0, "sys" : 0}
        ratios_avg["cold / local"] = {"real" : 0, "user" : 0, "sys" : 0}
        ratios_avg["warm / local"] = {"real" : 0, "user" : 0, "sys" : 0}
        ratios_avg["hot / local"] = {"real" : 0, "user" : 0, "sys" : 0}
        best_ratio_estimator_1["local"] = [0, 0]
        best_ratio_estimator_2["cold / local"] = {"real" : [0, 0], "user" : [0, 0], "sys" : [0, 0]}
        best_ratio_estimator_2["warm / local"] = {"real" : [0, 0], "user" : [0, 0], "sys" : [0, 0]}
        best_ratio_estimator_2["hot / local"] = {"real" : [0, 0], "user" : [0, 0], "sys" : [0, 0]}
        parse_file(tmp_dir + "/local_results", results["local"])

    # Compute average times
    for cache_type in results_avg.keys():
        for time_type in results_avg[cache_type].keys():
            for i in range(rounds):
                results_avg[cache_type][time_type] += results[cache_type][time_type][i]
            results_avg[cache_type][time_type] /= rounds
    print("{:<20} {:<15} {:<15} {:<15}".format("Cache_Type", "Real Avg (s)", "User Avg (s)",
        "Sys Avg (s)"))

    for cache_type in results_avg.keys():
        print("{:<20} {:<15.3f} {:<15.3f} {:<15.3f}".format(cache_type,
            results_avg[cache_type]["real"], results_avg[cache_type]["user"],
            results_avg[cache_type]["sys"]))
    print("-----------------------------------------------------------")

    # Compute time percantage on CPU and blocked
    for i in range(rounds):
        for cache_type in results_avg.keys():
            user_sys = results[cache_type]["user"][i] + results[cache_type]["sys"][i]
            best_ratio_estimator_1[cache_type][0] += user_sys *  results[cache_type]["real"][i]

            real_real = results[cache_type]["real"][i] * results[cache_type]["real"][i]
            best_ratio_estimator_1[cache_type][1] += real_real

    print("{:<20} {:<15} {:<15}".format("Cache_Type","CPU %","BLOCKED %"))
    for cache_type in results_avg.keys():
        cpu_fraction = best_ratio_estimator_1[cache_type][0] / best_ratio_estimator_1[cache_type][1]
        blocked_fraction = 1.0 - cpu_fraction
        print("{:<20} {:<15.3f} {:<15.3f}".format(cache_type, cpu_fraction, blocked_fraction))
    print("-----------------------------------------------------------")

    # Compute average ratios between different type of caches
    for compare_case in ratios_avg.keys():
        for time_type in ratios_avg[compare_case].keys():
            for i in range(rounds):
                nominator = results[compare_case.split(" / ")[0]][time_type][i]
                nominator *= results[compare_case.split(" / ")[1]][time_type][i]
                best_ratio_estimator_2[compare_case][time_type][0] += nominator
                denominator = results[compare_case.split(" / ")[1]][time_type][i]
                denominator *= results[compare_case.split(" / ")[1]][time_type][i]
                best_ratio_estimator_2[compare_case][time_type][1] += denominator
            ratios_avg[compare_case][time_type] = best_ratio_estimator_2[compare_case][time_type][0]
            ratios_avg[compare_case][time_type] /=best_ratio_estimator_2[compare_case][time_type][1]

    print("{:<20} {:<15} {:<15} {:<15}".format('Compare Case','Real Time','User Time', 'Sys Time'))
    for compare_case in ratios_avg.keys():
        print("{:<20} {:<15.3f} {:<15.3f} {:<15.3f}".format(compare_case,
            ratios_avg[compare_case]["real"], ratios_avg[compare_case]["user"],
            ratios_avg[compare_case]["sys"]))
