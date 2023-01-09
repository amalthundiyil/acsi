/**
 * This file is part of the CernVM File System.
 *
 * TelemetryAggregator class is a thread that snapshots the internal counters
 * of cvmfs statistic object. A custom telemetry class is needed to perform
 * the step of manipulating and sending/storing the counters.
 */

#ifndef CVMFS_TELEMETRY_AGGREGATOR_H_
#define CVMFS_TELEMETRY_AGGREGATOR_H_

#include <pthread.h>
#include <stdint.h>

#include <cstring>
#include <map>
#include <string>
#include <vector>

#include "options.h"
#include "statistics.h"
#include "util/single_copy.h"

namespace perf {

// Errors of telemetry classes (including custom classes)
enum TelemetryError {
  kTelemetrySuccess = 0,
  kTelemetryHostAddress,
  kTelemetryPort,
  kTelemetrySocket,
  kTelemetrySend
};

// List of available custom telemetry classes
enum TelemetrySelector {
  kTelemetryInflux
};

class TelemetryAggregator : SingleCopy {
 public:
  /**
   * Creates the requested telemetry aggregator. This function is also used to
   * register new classes to aggregate telemetry.
   *
   * Returns the newly created TelemetryAggregator or NULL if the creation
   * was not successful.
  */
  static TelemetryAggregator* Create(Statistics* statistics,
                                     int send_rate,
                                     OptionsManager *options_mgr,
                                     const std::string &fqrn,
                                     const TelemetrySelector type);
  virtual ~TelemetryAggregator();

  /**
   * Spawns the telemetry thread.
  */
  void Spawn();

 protected:
  Statistics* statistics_;
  const int maximum_send_rate_;
  int pipe_terminate_[2];
  pthread_t thread_telemetry_;
  std::string fqrn_;
  // State of constructed object. Used in custom telemetry classes to
  // specify that the object was correctly constructed.
  bool is_zombie_;

  uint64_t timestamp_;
  std::map<std::string, int64_t> counters_;

  /**
   * Main loop executed by the telemetry thread.
   * Checks every x seconds if the telemetry thread should continue running.
   * If yes, takes a snapshot of all statistic counters that are not 0 and
   * calls PushMetrics()
   *
   * PushMetrics() is defined by the custom telemetry classes and performs all
   * operation on the statistic counters to send/store them.
   *
  */
  static void *MainTelemetry(void *data);

  /**
   * Base constructor taking care of threading infrastructure.
   * Must always be called in the constructor of the custom telemetry classes.
  */
  TelemetryAggregator(Statistics* statistics, int maximum_send_rate,
                      const std::string &fqrn) :
                      statistics_(statistics),
                      maximum_send_rate_(maximum_send_rate),
                      fqrn_(fqrn),
                      is_zombie_(true),
                      timestamp_(0) {
    pipe_terminate_[0] = pipe_terminate_[1] = -1;
    memset(&thread_telemetry_, 0, sizeof(thread_telemetry_));
  }

  /**
   * PushMetrics is called after the snapshot of the counters.
   * It should perform all manipulation needed for the counters and the
   * sending/storing of the counters.
   *
   * Needs to be implemented in the custom telemetry class.
  */
  virtual void PushMetrics() = 0;
};

}  // namespace perf

#endif  // CVMFS_TELEMETRY_AGGREGATOR_H_
